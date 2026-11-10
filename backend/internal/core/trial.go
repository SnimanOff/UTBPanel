package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// A trial is a test run of a new version of one bot under another bot — the
// test bot: its own token, port, database and files, so real users never see
// the new version. The version is copied into the test bot's releases, the
// test bot is switched to it and started. When the owner is satisfied the
// trial is promoted: the usual deploy stops the current version of the real
// bot and starts the new one (backup, observation and autorollback included).
//
// The record is kept in kv under "trial:<bot id>"; the last test bot chosen
// for a bot under "trial_bot:<bot id>".

// TrialReq starts, promotes or stops a trial.
type TrialReq struct {
	BotID     int64  `cbor:"1,keyasint"`
	Version   string `cbor:"2,keyasint,omitempty"`
	TestBotID int64  `cbor:"3,keyasint,omitempty"`
	// CopyConfig gives the test bot the variables (env, rename, limits,
	// updates mode) and secrets of the real bot; its own BOT_TOKEN and
	// WEBHOOK_SECRET stay.
	CopyConfig bool `cbor:"4,keyasint,omitempty"`
	// ConfigVersion confirms the diff of the release's tgpanel.yaml on
	// promote, as in SwitchVersion.
	ConfigVersion string `cbor:"5,keyasint,omitempty"`
	// KeepTestBot leaves the test bot running after promote or stop.
	KeepTestBot bool          `cbor:"6,keyasint,omitempty"`
	Actor       journal.Actor `cbor:"9,keyasint"`
}

// Trial is the state of a test run shown in the panel.
type Trial struct {
	BotID       int64     `cbor:"1,keyasint" json:"bot_id"`
	Version     string    `cbor:"2,keyasint" json:"version"`
	TestBotID   int64     `cbor:"3,keyasint" json:"test_bot_id"`
	TestBot     string    `cbor:"4,keyasint" json:"test_bot"`      // panel name
	TestUser    string    `cbor:"5,keyasint" json:"test_username"` // @username without @
	TestVersion string    `cbor:"6,keyasint" json:"test_version"`  // the name in the test bot's releases
	Copied      bool      `cbor:"7,keyasint" json:"copied"`
	StartedAt   time.Time `cbor:"8,keyasint" json:"started_at"`
	Actor       string    `cbor:"9,keyasint" json:"actor"`
	// Error is why the test bot did not start; the trial stays so its logs
	// can be read and a fixed version tried again.
	Error string `cbor:"10,keyasint,omitempty" json:"error,omitempty"`
	// Process is the test bot's process now; Restarts counts crashes since
	// the trial started.
	Process  runner.Status `cbor:"11,keyasint" json:"process"`
	Restarts int           `cbor:"12,keyasint" json:"restarts"`
	// Restarts0 is the test bot's restart counter at the start.
	Restarts0 int `cbor:"13,keyasint" json:"restarts0"`
}

// TrialInfo is the trial of a bot (nil when none) and the test bot the
// panel should offer by default.
type TrialInfo struct {
	Trial       *Trial `cbor:"1,keyasint" json:"trial"`
	LastTestBot int64  `cbor:"2,keyasint" json:"last_test_bot,omitempty"`
}

func trialKey(id int64) string    { return "trial:" + strconv.FormatInt(id, 10) }
func trialBotKey(id int64) string { return "trial_bot:" + strconv.FormatInt(id, 10) }

func (c *Core) kvSet(ctx context.Context, key, value string) error {
	_, err := c.DB.W.ExecContext(ctx, `INSERT INTO kv (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (c *Core) kvDelete(ctx context.Context, key string) error {
	_, err := c.DB.W.ExecContext(ctx, `DELETE FROM kv WHERE key = ?`, key)
	return err
}

func (c *Core) loadTrial(ctx context.Context, botID int64) (*Trial, error) {
	v, err := c.kvGet(ctx, trialKey(botID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var t Trial
	if err := json.Unmarshal([]byte(v), &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (c *Core) saveTrial(ctx context.Context, t *Trial) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return c.kvSet(ctx, trialKey(t.BotID), string(b))
}

// TrialOf returns the trial of a bot with the test bot's process now.
func (c *Core) TrialOf(ctx context.Context, botID int64) (TrialInfo, error) {
	var out TrialInfo
	if err := c.botExists(ctx, botID); err != nil {
		return out, err
	}
	if v, err := c.kvGet(ctx, trialBotKey(botID)); err == nil {
		out.LastTestBot, _ = strconv.ParseInt(v, 10, 64)
	}
	t, err := c.loadTrial(ctx, botID)
	if err != nil || t == nil {
		return out, err
	}
	t.Process = c.Runner.Status(t.TestBotID)
	t.Restarts = max(0, t.Process.Restarts-t.Restarts0)
	out.Trial = t
	return out, nil
}

// trialOwner returns the bot whose trial runs on testBotID, 0 when none.
func (c *Core) trialOwner(ctx context.Context, testBotID int64) int64 {
	rows, err := c.DB.R.QueryContext(ctx, `SELECT key, value FROM kv WHERE key LIKE 'trial:%'`)
	if err != nil {
		return 0
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) != nil {
			continue
		}
		var t Trial
		if json.Unmarshal([]byte(v), &t) == nil && t.TestBotID == testBotID {
			return t.BotID
		}
	}
	return 0
}

// trialKeep are the secrets of the test bot itself: never copied over and
// never removed.
func trialKeep(name string) bool {
	return name == secrets.TokenName || name == secrets.WebhookSecretName
}

// trialSettings are the sections of bots.settings the runner reads.
var trialSettings = []string{"env", "rename", "limits", "updates"}

// copyTrialConfig gives the test bot the variables and secrets of the bot.
func (c *Core) copyTrialConfig(ctx context.Context, from, to int64) error {
	var raw string
	if err := c.DB.R.QueryRowContext(ctx, `SELECT settings FROM bots WHERE id = ?`, from).Scan(&raw); err != nil {
		return err
	}
	var src map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &src); err != nil {
		src = map[string]json.RawMessage{}
	}
	for _, k := range trialSettings {
		if v, ok := src[k]; ok {
			if err := c.SetSetting(ctx, to, k, v); err != nil {
				return err
			}
			continue
		}
		if _, err := c.DB.W.ExecContext(ctx, `UPDATE bots SET settings = json_remove(settings, '$.' || ?) WHERE id = ?`, k, to); err != nil {
			return err
		}
	}
	secs, err := c.Sec.DecryptAll(ctx, from)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, s := range secs {
		if trialKeep(s.Name) {
			continue
		}
		have[s.Name] = true
		if err := c.Sec.Set(ctx, to, s.Name, s.Kind, s.Value); err != nil {
			return err
		}
	}
	// What is left from an earlier trial of another bot goes away.
	old, err := c.Sec.List(ctx, to)
	if err != nil {
		return err
	}
	for _, s := range old {
		if !trialKeep(s.Name) && !have[s.Name] {
			if err := c.Sec.Delete(ctx, to, s.Name); err != nil && !errors.Is(err, secrets.ErrNotFound) {
				return err
			}
		}
	}
	return nil
}

// StartTrial runs a version of a bot on the test bot.
func (c *Core) StartTrial(ctx context.Context, in TrialReq) (*Trial, error) {
	b, err := c.Bot(ctx, in.BotID)
	if err != nil {
		return nil, err
	}
	if in.TestBotID == 0 || in.TestBotID == in.BotID {
		return nil, wire.Errorf(wire.CodeBadRequest, "выберите другой бот как тестовый")
	}
	tb, err := c.Bot(ctx, in.TestBotID)
	if err != nil {
		return nil, wire.Errorf(wire.CodeNotFound, "тестового бота %d нет", in.TestBotID)
	}
	if in.Version == "" {
		return nil, wire.Errorf(wire.CodeBadRequest, "укажите версию")
	}
	if !c.Releases.Exists(in.BotID, in.Version) {
		return nil, wire.Errorf(wire.CodeNotFound, "нет версии %s", in.Version)
	}
	if _, err := c.Sec.Decrypt(ctx, tb.ID, secrets.TokenName); err != nil {
		return nil, wire.Errorf(wire.CodeBadRequest, "у тестового бота %s нет токена", tb.Name)
	}
	if owner := c.trialOwner(ctx, tb.ID); owner != 0 && owner != in.BotID {
		return nil, wire.Errorf("conflict", "на боте %s уже идёт тест другого бота (%d)", tb.Name, owner)
	}
	if t, _ := c.loadTrial(ctx, tb.ID); t != nil {
		return nil, wire.Errorf("conflict", "бот %s сам сейчас тестируется на другом боте", tb.Name)
	}
	if owner := c.trialOwner(ctx, in.BotID); owner != 0 {
		return nil, wire.Errorf("conflict", "бот %s сейчас служит тестовым для бота %d", b.Name, owner)
	}
	// A new trial of the same bot replaces the old one; a different test
	// bot of the old trial is stopped.
	if old, _ := c.loadTrial(ctx, in.BotID); old != nil && old.TestBotID != tb.ID {
		c.Runner.Stop(ctx, old.TestBotID)
	}
	rel, err := c.Releases.CopyTo(in.BotID, in.Version, tb.ID)
	if err != nil {
		return nil, wire.Errorf("copy_failed", "версия %s не скопирована тестовому боту: %v", in.Version, err)
	}
	if c.Runner.IsRunning(tb.ID) {
		if err := c.Runner.Stop(ctx, tb.ID); err != nil {
			return nil, err
		}
	}
	if in.CopyConfig {
		if err := c.copyTrialConfig(ctx, in.BotID, tb.ID); err != nil {
			return nil, wire.Errorf("copy_failed", "переменные и секреты не скопированы: %v", err)
		}
	}
	if tb.Version != rel.Version {
		if _, err := c.DB.W.ExecContext(ctx, `UPDATE bots SET previous_version = current_version, current_version = ? WHERE id = ?`, rel.Version, tb.ID); err != nil {
			return nil, err
		}
	}
	cur, prev, _ := c.versions(ctx, tb.ID)
	c.Releases.Prune(tb.ID, cur, prev)
	c.kvSet(ctx, trialBotKey(in.BotID), strconv.FormatInt(tb.ID, 10))
	t := &Trial{
		BotID: in.BotID, Version: in.Version, TestBotID: tb.ID, TestBot: tb.Name, TestUser: tb.Username,
		TestVersion: rel.Version, Copied: in.CopyConfig, StartedAt: nowUTC(),
		Actor: in.Actor.Source + ":" + in.Actor.Name, Restarts0: c.Runner.Status(tb.ID).Restarts,
	}
	msg := "тест версии " + in.Version + " на боте " + tb.Name
	c.Logs.Add(in.BotID, "core", msg, nowUTC())
	c.Logs.Add(tb.ID, "core", "тестовый прогон версии "+in.Version+" бота "+b.Name, nowUTC())
	if err := c.StartBot(ctx, tb.ID); err != nil {
		t.Error = err.Error()
		var we *wire.Error
		if errors.As(err, &we) {
			t.Error = we.Message
		}
		c.Logs.Add(in.BotID, "core", msg+": не запустилась — "+t.Error, nowUTC())
	}
	if err := c.saveTrial(ctx, t); err != nil {
		return nil, err
	}
	sev := events.Info
	if t.Error != "" {
		sev = events.Warn
	}
	c.Ev.Emit("trial", in.BotID, sev, map[string]any{"action": "started", "version": in.Version, "test_bot": tb.ID, "error": t.Error})
	t.Process = c.Runner.Status(tb.ID)
	return t, nil
}

// StopTrial ends a trial: the test bot is stopped (unless keep), the real
// bot is not touched.
func (c *Core) StopTrial(ctx context.Context, botID int64, keep bool, reason string) error {
	t, err := c.loadTrial(ctx, botID)
	if err != nil {
		return err
	}
	if t == nil {
		return wire.Errorf(wire.CodeNotFound, "теста нет")
	}
	if !keep && c.Runner.IsRunning(t.TestBotID) {
		if err := c.Runner.Stop(ctx, t.TestBotID); err != nil {
			return err
		}
	}
	if err := c.kvDelete(ctx, trialKey(botID)); err != nil {
		return err
	}
	c.Logs.Add(botID, "core", "тест версии "+t.Version+" завершён: "+reason, nowUTC())
	c.Ev.Emit("trial", botID, events.Info, map[string]any{"action": "stopped", "version": t.Version, "reason": reason})
	return nil
}

// PromoteTrial deploys the tested version to the real bot: the current
// version stops, the new one starts, with a backup before and autorollback
// after. The trial ends when the deploy went through.
func (c *Core) PromoteTrial(ctx context.Context, in TrialReq) error {
	t, err := c.loadTrial(ctx, in.BotID)
	if err != nil {
		return err
	}
	if t == nil {
		return wire.Errorf(wire.CodeNotFound, "теста нет")
	}
	if !c.Releases.Exists(in.BotID, t.Version) {
		return wire.Errorf(wire.CodeNotFound, "версии %s больше нет", t.Version)
	}
	if _, err := c.Deploy(ctx, SwitchVersion{BotID: in.BotID, Version: t.Version, ConfigVersion: in.ConfigVersion, Actor: in.Actor}); err != nil {
		return err
	}
	return c.StopTrial(ctx, in.BotID, in.KeepTestBot, "выкачена на основного бота")
}

func (c *Core) registerTrial() {
	c.Mux.Handle("trial.get", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (TrialInfo, error) {
		return c.TrialOf(ctx, id)
	}))
	c.Mux.Handle("trial.start", wire.Func(func(ctx context.Context, _ *wire.Conn, in TrialReq) (t *Trial, err error) {
		err = c.Journal.Do(ctx, in.Actor, "trial.start", in.BotID, map[string]any{"version": in.Version, "test_bot": in.TestBotID, "copy_config": in.CopyConfig}, func() error {
			t, err = c.StartTrial(ctx, in)
			return err
		})
		return t, err
	}))
	c.Mux.Handle("trial.promote", wire.Func(func(ctx context.Context, _ *wire.Conn, in TrialReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "trial.promote", in.BotID, nil, func() error {
			return c.PromoteTrial(ctx, in)
		})
	}))
	c.Mux.Handle("trial.stop", wire.Func(func(ctx context.Context, _ *wire.Conn, in TrialReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "trial.stop", in.BotID, nil, func() error {
			return c.StopTrial(ctx, in.BotID, in.KeepTestBot, "отменён")
		})
	}))
}
