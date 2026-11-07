package core

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
	"github.com/SnimanOff/UTBPanel/backend/internal/users"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
	"github.com/SnimanOff/UTBPanel/backend/sdk/flags"
)

// ConfigSnapshot contains only portable settings, never secret values.
func (c *Core) ConfigSnapshot(ctx context.Context, id int64) (botconfig.Snapshot, error) {
	ctx, unlock, err := c.ConfigGuard.Lock(ctx)
	if err != nil {
		return botconfig.Snapshot{}, err
	}
	defer unlock()
	m, err := c.Settings(ctx, id)
	if err != nil {
		return botconfig.Snapshot{}, err
	}
	if len(m["profile_cache"]) == 0 {
		if _, err := c.Token(ctx, id); err == nil {
			read, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, err = c.Profile(read, id, "")
			cancel()
			if err != nil {
				return botconfig.Snapshot{}, err
			}
			m, err = c.Settings(ctx, id)
			if err != nil {
				return botconfig.Snapshot{}, err
			}
		}
	}
	d := botconfig.Document{Antiflood: users.DefaultFlood()}
	fields := map[string]any{"env": &d.Env, "rename": &d.Rename, "required_secrets": &d.Secrets, "antiflood": &d.Antiflood, "closed": &d.Closed, "texts": &d.Texts, "tz": &d.TZ, "profile_cache": &d.Profile, "limits": &d.Limits, "stop": &d.Stop, "log_patterns": &d.LogPatterns, "crash_updates": &d.CrashUpdates, "updates": &d.Updates}
	for key, target := range fields {
		if raw := m[key]; len(raw) > 0 {
			if err := json.Unmarshal(raw, target); err != nil {
				return botconfig.Snapshot{}, fmt.Errorf("settings %s: %w", key, err)
			}
		}
	}
	info, err := c.Sec.List(ctx, id)
	if err != nil {
		return botconfig.Snapshot{}, err
	}
	names := map[string]bool{}
	for _, name := range d.Secrets {
		names[name] = true
	}
	for _, s := range info {
		if !botconfig.ReservedSecret(s.Name) {
			names[s.Name] = true
		}
	}
	d.Secrets = nil
	for name := range names {
		d.Secrets = append(d.Secrets, name)
	}
	if raw := m["pending_flags"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &d.Flags); err != nil {
			return botconfig.Snapshot{}, err
		}
	}
	if d.Flags == nil {
		d.Flags = map[string]botconfig.FlagValue{}
	}
	if c.SDKFlags != nil {
		list, err := c.SDKFlags.List(ctx, id)
		if err != nil {
			return botconfig.Snapshot{}, err
		}
		for _, f := range list {
			if f.Overridden {
				d.Flags[f.Name] = botconfig.FlagValue(f.Value)
			}
		}
	}
	g, err := c.GitHub(ctx, id)
	if err != nil {
		return botconfig.Snapshot{}, err
	}
	if g != nil {
		d.GitHub = botconfig.GitHub{Repo: g.Repo, Asset: g.AssetMask, AutoDeploy: g.AutoDeploy}
	}
	botconfig.Normalize(&d)
	// A random encryption nonce changes even for two writes in one clock tick.
	// Only its digest contributes to the opaque revision; no secret is decrypted.
	rows, err := c.DB.R.QueryContext(ctx, `SELECT name,kind,nonce FROM secrets WHERE bot_id=? ORDER BY name`, id)
	if err != nil {
		return botconfig.Snapshot{}, err
	}
	revisions := map[string]string{}
	for rows.Next() {
		var name, kind string
		var nonce []byte
		if err := rows.Scan(&name, &kind, &nonce); err != nil {
			rows.Close()
			return botconfig.Snapshot{}, err
		}
		revisions[name] = kind + ":" + botconfig.Fingerprint(nonce)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return botconfig.Snapshot{}, err
	}
	version := botconfig.Fingerprint(struct {
		Document botconfig.Document
		Secrets  map[string]string
	}{d, revisions})
	return botconfig.Snapshot{Document: d, Version: version}, nil
}

func (c *Core) ApplyConfig(ctx context.Context, in botconfig.ApplyRequest) (botconfig.ApplyResult, error) {
	out := botconfig.ApplyResult{MissingSecrets: []string{}, ProfileErrors: map[string]string{}}
	ctx, unlock, err := c.ConfigGuard.Lock(ctx)
	if err != nil {
		return out, err
	}
	defer unlock()
	current, err := c.ConfigSnapshot(ctx, in.BotID)
	if err != nil {
		return out, err
	}
	if in.Version == "" || in.Version != current.Version {
		return out, wire.Errorf("conflict", "настройки изменились; перечитайте документ")
	}
	d := in.Document
	botconfig.Normalize(&d)
	if err := botconfig.Validate(d); err != nil {
		return out, wire.Errorf(wire.CodeBadRequest, "%v", err)
	}
	registered, err := c.SDKFlags.List(ctx, in.BotID)
	if err != nil {
		return out, err
	}
	kinds := map[string]string{}
	pending := map[string]botconfig.FlagValue{}
	stored, err := c.Settings(ctx, in.BotID)
	if err != nil {
		return out, err
	}
	if raw := stored["pending_flags"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &pending); err != nil {
			return out, err
		}
	}
	for _, f := range registered {
		kinds[f.Name] = f.Kind
	}
	for name, value := range d.Flags {
		kind, ok := kinds[name]
		if !ok {
			if _, waiting := pending[name]; waiting {
				pending[name] = value
				continue
			}
			return out, wire.Errorf(wire.CodeBadRequest, "flag %q is not registered", name)
		}
		normalized, err := flags.Normalize(kind, flags.Value(value))
		if err != nil {
			return out, wire.Errorf(wire.CodeBadRequest, "flag %s: %v", name, err)
		}
		d.Flags[name] = botconfig.FlagValue(normalized)
		delete(pending, name)
	}
	for name := range pending {
		if _, keep := d.Flags[name]; !keep {
			delete(pending, name)
		}
	}
	before := current.Document
	out.ProfileErrors = map[string]string{}
	// Telegram does not support transactions. Report individual refusals and
	// return the new revision of any fields it did accept before applying locals.
	if !reflect.DeepEqual(before.Profile, d.Profile) {
		langs := map[string]bool{}
		for _, p := range []botconfig.Profile{before.Profile, d.Profile} {
			for lang := range p.Name {
				langs[lang] = true
			}
			for lang := range p.Description {
				langs[lang] = true
			}
			for lang := range p.ShortDescription {
				langs[lang] = true
			}
			for lang := range p.Commands {
				langs[lang] = true
			}
		}
		if !reflect.DeepEqual(before.Profile.Menu, d.Profile.Menu) {
			langs["default"] = true
		}
		ordered := []string{}
		for lang := range langs {
			ordered = append(ordered, lang)
		}
		sort.Strings(ordered)
		for _, lang := range ordered {
			req := ProfileReq{BotID: in.BotID, Lang: lang}
			if lang == "default" {
				req.Lang = ""
			}
			changedString := func(a, b map[string]string) *string {
				old, okOld := a[lang]
				next, okNext := b[lang]
				if old == next && okOld == okNext {
					return nil
				}
				return &next
			}
			req.Name = changedString(before.Profile.Name, d.Profile.Name)
			req.Description = changedString(before.Profile.Description, d.Profile.Description)
			req.ShortDescription = changedString(before.Profile.ShortDescription, d.Profile.ShortDescription)
			if !reflect.DeepEqual(before.Profile.Commands[lang], d.Profile.Commands[lang]) {
				commands := d.Profile.Commands[lang]
				if commands == nil {
					commands = []telegram.BotCommand{}
				}
				req.Commands = &commands
			}
			if lang == "default" && !reflect.DeepEqual(before.Profile.Menu, d.Profile.Menu) {
				req.Menu = d.Profile.Menu
				if req.Menu == nil {
					req.Menu = &telegram.MenuButton{Type: "default"}
				}
			}
			saved, err := c.SaveProfile(ctx, req)
			if err != nil {
				return out, err
			}
			for field, problem := range saved.Errors {
				out.ProfileErrors[lang+"."+field] = problem.Message
			}
		}
		if len(out.ProfileErrors) > 0 {
			out.Snapshot, err = c.ConfigSnapshot(ctx, in.BotID)
			return out, err
		}
	}
	if !reflect.DeepEqual(before.GitHub, d.GitHub) {
		if _, err := c.SetGitHub(ctx, GitHubSet{BotID: in.BotID, Repo: d.GitHub.Repo, AssetMask: d.GitHub.Asset, AutoDeploy: d.GitHub.AutoDeploy}); err != nil {
			return out, err
		}
	}
	for name, value := range d.Flags {
		if kinds[name] == "" {
			continue
		}
		if old, ok := before.Flags[name]; !ok || old != value {
			if err := c.SDKFlags.Set(ctx, in.BotID, name, flags.Value(value), false); err != nil {
				return out, err
			}
		}
	}
	for name := range before.Flags {
		if kinds[name] == "" {
			continue
		}
		if _, ok := d.Flags[name]; !ok {
			if err := c.SDKFlags.Set(ctx, in.BotID, name, flags.Value{}, true); err != nil {
				return out, err
			}
		}
	}
	keep := map[string]bool{}
	for _, name := range d.Secrets {
		keep[name] = true
	}
	for _, name := range before.Secrets {
		if !keep[name] {
			if err := c.Sec.Delete(ctx, in.BotID, name); err != nil && err != secrets.ErrNotFound {
				return out, err
			}
		}
	}
	settings := documentSettings(d)
	settings["pending_flags"] = pending
	// Merge only represented keys, retaining internal deploy/maintenance state.
	query := `UPDATE bots SET settings=json_set(settings,`
	args := []any{}
	keys := []string{}
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw, err := json.Marshal(settings[key])
		if err != nil {
			return out, err
		}
		query += "'$." + key + "',json(?),"
		args = append(args, string(raw))
	}
	query = strings.TrimSuffix(query, ",") + ") WHERE id=?"
	args = append(args, in.BotID)
	if _, err = c.DB.W.ExecContext(ctx, query, args...); err != nil {
		return out, err
	}
	c.Flood.Invalidate(in.BotID)
	c.Closed.Invalidate(in.BotID)
	for key := range settings {
		for _, fn := range c.onSettings {
			fn(ctx, in.BotID, key)
		}
	}
	go c.PushEdge(context.Background())
	out.Restart = botconfig.Diff(before, d).Restart
	out.Applied = true
	info, err := c.Sec.List(ctx, in.BotID)
	if err != nil {
		return out, err
	}
	for _, s := range info {
		delete(keep, s.Name)
	}
	out.MissingSecrets = []string{}
	for name := range keep {
		out.MissingSecrets = append(out.MissingSecrets, name)
	}
	sort.Strings(out.MissingSecrets)
	out.Snapshot, err = c.ConfigSnapshot(ctx, in.BotID)
	return out, err
}

func documentSettings(d botconfig.Document) map[string]any {
	return map[string]any{"env": d.Env, "rename": d.Rename, "required_secrets": d.Secrets, "antiflood": d.Antiflood, "closed": d.Closed, "texts": d.Texts, "tz": d.TZ, "profile_cache": d.Profile, "limits": d.Limits, "stop": d.Stop, "log_patterns": d.LogPatterns, "crash_updates": d.CrashUpdates, "updates": d.Updates}
}

func (c *Core) registerConfig() {
	c.Mux.Handle("config.get", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (botconfig.Snapshot, error) {
		return c.ConfigSnapshot(ctx, id)
	}))
	c.Mux.Handle("config.apply", wire.Func(func(ctx context.Context, _ *wire.Conn, in botconfig.ApplyRequest) (botconfig.ApplyResult, error) {
		var out botconfig.ApplyResult
		err := c.Journal.Do(ctx, in.Actor, "config.apply", in.BotID, nil, func() error { var err error; out, err = c.ApplyConfig(ctx, in); return err })
		return out, err
	}))
}
