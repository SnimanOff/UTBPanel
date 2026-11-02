package core

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// applyReleaseConfig brings the settings of tgpanel.yaml in before a
// version starts. Changes of the "with confirmation" level stop the deploy
// until the owner confirms the diff, even with autodeploy: in.ConfigVersion
// is the settings revision the confirmed diff was made against.
// Alert thresholds belong to the panel: they leave in the event.
func (c *Core) applyReleaseConfig(ctx context.Context, in SwitchVersion) error {
	if in.Actor.Source == journal.SourceAutorollback {
		return nil // going back must not wait for anyone
	}
	raw, err := c.Releases.Config(in.BotID, in.Version)
	if err != nil || raw == nil {
		return err
	}
	invalid := func(err error) error {
		c.Ev.Emit("release_config", in.BotID, events.Error, map[string]any{"version": in.Version, "state": "invalid", "error": err.Error()})
		return wire.Errorf("bad_config", "tgpanel.yaml версии %s: %v", in.Version, err)
	}
	file, err := botconfig.ParseRelease(raw)
	if err != nil {
		return invalid(err)
	}
	ctx, unlock, err := c.ConfigGuard.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := c.ConfigSnapshot(ctx, in.BotID)
	if err != nil {
		return err
	}
	next, err := file.Over(current.Document)
	if err != nil {
		return invalid(err)
	}
	next.Alerts = current.Document.Alerts
	diff := botconfig.Diff(current.Document, next)
	if diff.Confirm && in.ConfigVersion != current.Version {
		if in.ConfigVersion != "" {
			return wire.Errorf("conflict", "настройки изменились после просмотра diff; посмотрите его снова")
		}
		paths := []string{}
		for _, ch := range diff.Changes {
			if ch.Level == "confirm" {
				paths = append(paths, ch.Path)
			}
		}
		sort.Strings(paths)
		c.Ev.Emit("release_config", in.BotID, events.Warn, map[string]any{"version": in.Version, "state": "pending", "paths": strings.Join(paths, ", ")})
		return wire.Errorf("config_confirm", "tgpanel.yaml версии %s меняет %s: подтвердите diff во вкладке «Деплой»", in.Version, strings.Join(paths, ", "))
	}
	if len(diff.Changes) > 0 {
		var res botconfig.ApplyResult
		err := c.Journal.Do(ctx, in.Actor, "config.apply", in.BotID, map[string]string{"source": "tgpanel.yaml", "version": in.Version}, func() error {
			var err error
			res, err = c.ApplyConfig(ctx, botconfig.ApplyRequest{BotID: in.BotID, Version: current.Version, Document: next, Actor: in.Actor})
			return err
		})
		if err != nil {
			return err
		}
		if !res.Applied {
			problems := []string{}
			for field, msg := range res.ProfileErrors {
				problems = append(problems, field+": "+msg)
			}
			sort.Strings(problems)
			return wire.Errorf("bad_config", "Telegram не принял профиль из tgpanel.yaml: %s", strings.Join(problems, "; "))
		}
	}
	data := map[string]any{"version": in.Version, "state": "applied", "changes": len(diff.Changes)}
	if file.Alerts != nil {
		rules, _ := json.Marshal(*file.Alerts)
		data["alerts"] = string(rules)
	}
	c.Ev.Emit("release_config", in.BotID, events.Info, data)
	return nil
}

func (c *Core) registerReleaseConfig() {
	c.Mux.Handle("releases.config", wire.Func(func(ctx context.Context, _ *wire.Conn, in botconfig.ReleaseRequest) (string, error) {
		if err := c.botExists(ctx, in.BotID); err != nil {
			return "", err
		}
		if !c.Releases.Exists(in.BotID, in.Version) {
			return "", wire.Errorf(wire.CodeNotFound, "no version %s", in.Version)
		}
		raw, err := c.Releases.Config(in.BotID, in.Version)
		return string(raw), err
	}))
}
