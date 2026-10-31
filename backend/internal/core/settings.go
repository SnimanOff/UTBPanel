package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// SettingsUpdate replaces parts of a bot's settings; nil fields stay.
type SettingsUpdate struct {
	BotID  int64              `cbor:"1,keyasint"`
	Env    *map[string]string `cbor:"2,keyasint,omitempty"`
	Rename *map[string]string `cbor:"3,keyasint,omitempty"`
	Limits *runner.Limits     `cbor:"4,keyasint,omitempty"`
	Actor  journal.Actor      `cbor:"9,keyasint"`
}

// Settings returns the stored settings document of a bot as raw JSON
// (other packages keep their own keys in it).
func (c *Core) Settings(ctx context.Context, id int64) (map[string]json.RawMessage, error) {
	var raw string
	if err := c.DB.R.QueryRowContext(ctx, `SELECT settings FROM bots WHERE id = ?`, id).Scan(&raw); err != nil {
		return nil, errNoBot
	}
	m := map[string]json.RawMessage{}
	json.Unmarshal([]byte(raw), &m)
	return m, nil
}

// SetSetting stores one key of the settings document.
func (c *Core) SetSetting(ctx context.Context, id int64, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	res, err := c.DB.W.ExecContext(ctx, `UPDATE bots SET settings = json_set(settings, '$.' || ?, json(?)) WHERE id = ?`, key, string(b), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNoBot
	}
	return nil
}

func validEnv(m map[string]string) error {
	for k := range m {
		if !secrets.ValidName(k) {
			return wire.Errorf(wire.CodeBadRequest, "bad variable name %q", k)
		}
		if reserved(k) {
			return wire.Errorf(wire.CodeBadRequest, "%s is set by the panel", k)
		}
	}
	return nil
}

// UpdateSettings applies env, renames and limits. They take effect after a
// restart of the bot.
func (c *Core) UpdateSettings(ctx context.Context, in SettingsUpdate) error {
	if err := c.botExists(ctx, in.BotID); err != nil {
		return err
	}
	if in.Env != nil {
		if err := validEnv(*in.Env); err != nil {
			return err
		}
		if err := c.SetSetting(ctx, in.BotID, "env", *in.Env); err != nil {
			return err
		}
	}
	if in.Rename != nil {
		for from, to := range *in.Rename {
			if !secrets.ValidName(from) || (to != "" && !secrets.ValidName(to)) {
				return wire.Errorf(wire.CodeBadRequest, "bad variable name in %s → %s", from, to)
			}
		}
		if err := c.SetSetting(ctx, in.BotID, "rename", *in.Rename); err != nil {
			return err
		}
	}
	if in.Limits != nil {
		l := *in.Limits
		if l.Memory < 0 || l.File < 0 || l.FilesTotal < 0 || l.OpenFiles < 0 || l.Threads < 0 {
			return wire.Errorf(wire.CodeBadRequest, "limits cannot be negative")
		}
		if err := c.SetSetting(ctx, in.BotID, "limits", l); err != nil {
			return err
		}
	}
	return nil
}

// DeleteBot stops the bot, removes its webhook and deletes all its data.
func (c *Core) DeleteBot(ctx context.Context, id int64) error {
	if err := c.botExists(ctx, id); err != nil {
		return err
	}
	c.Runner.Stop(ctx, id)
	if tok, err := c.Token(ctx, id); err == nil {
		if err := c.TG.DeleteWebhook(ctx, tok, false); err != nil {
			c.Log.Warn("deleteWebhook of a deleted bot failed", "bot", id, "err", err)
		}
	}
	if _, err := c.DB.W.ExecContext(ctx, `DELETE FROM bots WHERE id = ?`, id); err != nil {
		return err
	}
	c.Logs.Remove(id)
	dir := filepath.Join(c.Cfg.DataDir, "bots", strconv.FormatInt(id, 10))
	return os.RemoveAll(dir)
}

func (c *Core) registerSettings() {
	c.Mux.Handle("bots.settings", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (map[string]json.RawMessage, error) {
		return c.Settings(ctx, id)
	}))
	c.Mux.Handle("bots.settings.update", wire.Func(func(ctx context.Context, _ *wire.Conn, in SettingsUpdate) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "bots.settings.update", in.BotID, nil, func() error {
			return c.UpdateSettings(ctx, in)
		})
	}))
	c.Mux.Handle("bots.delete", wire.Func(func(ctx context.Context, _ *wire.Conn, in BotRef) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "bots.delete", in.BotID, nil, func() error {
			return c.DeleteBot(ctx, in.BotID)
		})
	}))
	c.Mux.Handle("bots.status", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (runner.Status, error) {
		return c.Runner.Status(id), nil
	}))
}
