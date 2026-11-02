package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// storeRelease puts a version with tgpanel.yaml in place without a real
// binary: the tests look at settings, not at whether the bot starts.
func storeRelease(t *testing.T, c *Core, botID int64, version, config string) {
	t.Helper()
	dir := filepath.Join(c.Releases.Dir(botID), version)
	os.MkdirAll(dir, 0o750)
	os.WriteFile(filepath.Join(dir, "bot"), []byte("x"), 0o500)
	meta, _ := json.Marshal(deploy.Release{Version: version, Source: "github"})
	os.WriteFile(filepath.Join(dir, "release.json"), meta, 0o640)
	if config != "" {
		if err := c.Releases.SetConfig(botID, version, []byte(config)); err != nil {
			t.Fatal(err)
		}
	}
}

func code(err error) string {
	var e *wire.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestReleaseConfigOnDeploy(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	withTelegram(t, c)
	b, err := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:good"})
	if err != nil {
		t.Fatal(err)
	}
	// The fake Telegram has no profile methods: the settings use a cached one.
	c.DB.W.Exec(`UPDATE bots SET settings = json_set(settings, '$.profile_cache', json('{}')) WHERE id = ?`, b.ID)
	snap := func() string {
		s, err := c.ConfigSnapshot(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		return s.Version
	}

	// Only "write" fields: applied with the deploy; absent sections stay.
	storeRelease(t, c, b.ID, "v1", "tz: Europe/Berlin\ntexts: { maintenance: Ремонт }\n")
	if rs, _ := c.Releases.List(b.ID, "", ""); len(rs) != 1 || !rs[0].Config {
		t.Fatalf("releases %+v", rs)
	}
	c.Deploy(ctx, SwitchVersion{BotID: b.ID, Version: "v1", Actor: githubActor})
	s, _ := c.ConfigSnapshot(ctx, b.ID)
	if s.Document.TZ != "Europe/Berlin" || s.Document.Texts["maintenance"] != "Ремонт" {
		t.Fatalf("not applied: %+v", s.Document)
	}

	// env needs confirmation, even with autodeploy.
	storeRelease(t, c, b.ID, "v2", "env: { LOG_LEVEL: debug }\n")
	before := snap()
	_, err = c.Deploy(ctx, SwitchVersion{BotID: b.ID, Version: "v2", Actor: githubActor})
	if code(err) != "config_confirm" {
		t.Fatalf("want config_confirm, got %v", err)
	}
	if cur, _, _ := c.versions(ctx, b.ID); cur == "v2" {
		t.Fatal("deployed without confirmation")
	}
	if snap() != before {
		t.Fatal("settings changed without confirmation")
	}
	// A confirmation of another revision is refused.
	if _, err = c.Deploy(ctx, SwitchVersion{BotID: b.ID, Version: "v2", ConfigVersion: "stale", Actor: githubActor}); code(err) != "conflict" {
		t.Fatalf("want conflict, got %v", err)
	}
	c.Deploy(ctx, SwitchVersion{BotID: b.ID, Version: "v2", ConfigVersion: before, Actor: journal.Actor{Source: "panel", Name: "owner"}})
	s, _ = c.ConfigSnapshot(ctx, b.ID)
	if s.Document.Env["LOG_LEVEL"] != "debug" || s.Document.TZ != "Europe/Berlin" {
		t.Fatalf("after confirm: env %v tz %s", s.Document.Env, s.Document.TZ)
	}

	// A broken file stops the deploy, but never an autorollback.
	storeRelease(t, c, b.ID, "v3", "name: echo\n")
	if _, err = c.Deploy(ctx, SwitchVersion{BotID: b.ID, Version: "v3", Actor: githubActor}); code(err) != "bad_config" {
		t.Fatalf("want bad_config, got %v", err)
	}
	if err := c.applyReleaseConfig(ctx, SwitchVersion{BotID: b.ID, Version: "v3", Actor: journal.Actor{Source: journal.SourceAutorollback}}); err != nil {
		t.Fatal(err)
	}

	// Without the file everything works as before.
	storeRelease(t, c, b.ID, "v4", "")
	if err := c.applyReleaseConfig(ctx, SwitchVersion{BotID: b.ID, Version: "v4", Actor: githubActor}); err != nil {
		t.Fatal(err)
	}
}
