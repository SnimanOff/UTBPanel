package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

func TestCanaryChecksAndRoute(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	withTelegram(t, c)
	b, _ := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:good"})
	if err := c.StartCanary(ctx, CanaryReq{BotID: b.ID, Version: "v2", Share: 10}); err == nil {
		t.Fatal("canary next to a stopped bot")
	}
	if err := c.StartCanary(ctx, CanaryReq{BotID: b.ID, Version: "v2", Share: 101}); err == nil {
		t.Fatal("share over 100 %")
	}

	c.setRoute(b.ID, &canaryRoute{port: 31000, users: userSet([]int64{7})})
	u := &ingress.Update{BotID: b.ID, ChatType: "private"}
	u.UserID = 7
	if port, ok := c.route(u); !ok || port != 31000 {
		t.Fatal("listed user not routed to the canary")
	}
	u.UserID = 8
	if _, ok := c.route(u); ok {
		t.Fatal("share 0: others stay on main")
	}
	c.setRoute(b.ID, nil)
	u.UserID = 7
	if _, ok := c.route(u); ok {
		t.Fatal("route left after stop")
	}
	if cn, err := c.CanaryOf(ctx, b.ID); cn != nil || err != nil {
		t.Fatalf("%v %v", cn, err)
	}
}

func TestCanaryVerdict(t *testing.T) {
	r := deploy.DefaultRules
	if v, _ := CanaryVerdict(r, 6, 4, 100, 0, time.Minute, 0); v != "cancel" {
		t.Fatalf("40%% errors: %q", v)
	}
	if v, _ := CanaryVerdict(r, 1, 2, 100, 0, time.Minute, 0); v != "" {
		t.Fatal("too few updates to judge")
	}
	if v, _ := CanaryVerdict(r, 100, 1, 100, 1, 31*time.Minute, 30*time.Minute); v != "promote" {
		t.Fatalf("no worse than main after 30 min: %q", v)
	}
	if v, _ := CanaryVerdict(r, 90, 9, 100, 0, 31*time.Minute, 30*time.Minute); v != "" {
		t.Fatalf("more errors than main must not promote: %q", v)
	}
	if v, _ := CanaryVerdict(r, 100, 0, 100, 0, 10*time.Minute, 30*time.Minute); v != "" {
		t.Fatal("promoted too early")
	}
	for _, observed := range []int{0, r.MinUpdates - 1} {
		if v, _ := CanaryVerdict(r, observed, 0, 100, 0, 31*time.Minute, 30*time.Minute); v != "" {
			t.Fatalf("auto-promoted with only %d observed updates: %q", observed, v)
		}
	}
}

func TestCheckCanaryDoesNotPromoteExitedProcess(t *testing.T) {
	ctx := t.Context()
	c := newCore(t)
	addCanaryCandidate(t, c, "")
	c.canaryStatsOf(1).ok = 100
	c.checkCanary(ctx, 1)
	current, _, err := c.versions(ctx, 1)
	if err != nil || current != "v1" {
		t.Fatalf("after checking exited canary, current = %q, err = %v", current, err)
	}
}

func TestManualCanaryPromotionPreservesCandidateOnUnconfirmedRelease(t *testing.T) {
	ctx := t.Context()
	c := newCore(t)
	addCanaryCandidate(t, c, "env: { LOG_LEVEL: debug }\n")
	err := c.PromoteCanary(ctx, 1, journal.Actor{Source: journal.SourceUI})
	if code(err) != "config_confirm" {
		t.Fatalf("promoting unconfirmed release: %v", err)
	}
	candidate, err := c.CanaryOf(ctx, 1)
	if err != nil || candidate == nil || candidate.Version != "v2" {
		t.Fatalf("candidate after validation failure: %+v, err = %v", candidate, err)
	}
}

func TestManualCanaryPromotionDoesNotRequireObservations(t *testing.T) {
	ctx := t.Context()
	c := newCore(t)
	addCanaryCandidate(t, c, "")
	if err := c.PromoteCanary(ctx, 1, journal.Actor{Source: journal.SourceUI}); err != nil {
		t.Fatal(err)
	}
	current, _, err := c.versions(ctx, 1)
	if err != nil || current != "v2" {
		t.Fatalf("after manual promotion, current = %q, err = %v", current, err)
	}
	candidate, err := c.CanaryOf(ctx, 1)
	if err != nil || candidate != nil {
		t.Fatalf("candidate after manual promotion: %+v, err = %v", candidate, err)
	}
}

func TestAutoPromotionWaitsQuietlyForReleaseConfirmation(t *testing.T) {
	ctx := t.Context()
	c := newCore(t)
	addCanaryCandidate(t, c, "env: { LOG_LEVEL: debug }\n")
	in := SwitchVersion{BotID: 1, Version: "v2", Actor: canaryActor}
	stats := c.canaryStatsOf(in.BotID)
	promote := func() error {
		_, err := c.Deploy(ctx, in)
		return err
	}
	for range 3 {
		c.autoPromoteCanary(ctx, stats, in, promote)
	}

	entries, err := c.Journal.Since(ctx, 0, 100)
	if err != nil || len(entries) != 1 || entries[0].Command != "canary.promote" {
		t.Fatalf("journal while confirmation is pending: %+v, err = %v", entries, err)
	}
	events, err := c.Ev.Since(ctx, 0, 100)
	if err != nil || len(events) != 1 || events[0].Type != "release_config" || events[0].Data["state"] != "pending" {
		t.Fatalf("events while confirmation is pending: %+v, err = %v", events, err)
	}
	if err := c.SetSetting(ctx, in.BotID, "env", map[string]string{"LOG_LEVEL": "debug"}); err != nil {
		t.Fatal(err)
	}
	c.autoPromoteCanary(ctx, stats, in, promote)
	current, _, err := c.versions(ctx, in.BotID)
	if err != nil || current != "v2" {
		t.Fatalf("after resolving the configuration, current = %q, err = %v", current, err)
	}
}

func TestCanaryStopRequestWaitsForDeployment(t *testing.T) {
	ctx := t.Context()
	c := newCore(t)
	addCanaryCandidate(t, c, "")
	_, unlock, err := c.lockDeploy(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	stopCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	err = c.requestStopCanary(stopCtx, 1, "test")
	cancel()
	unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop during deployment: %v", err)
	}
	if candidate, err := c.CanaryOf(ctx, 1); err != nil || candidate == nil {
		t.Fatalf("candidate after timed-out stop: %+v, err = %v", candidate, err)
	}
	for _, botID := range []int64{1, 1, 2} {
		if err := c.requestStopCanary(ctx, botID, "test"); err != nil {
			t.Fatalf("stop for bot %d: %v", botID, err)
		}
	}
	if candidate, err := c.CanaryOf(ctx, 1); err != nil || candidate != nil {
		t.Fatalf("candidate after successful stop: %+v, err = %v", candidate, err)
	}
}

func addCanaryCandidate(t *testing.T, c *Core, config string) {
	t.Helper()
	ctx := t.Context()
	if _, err := c.DB.W.ExecContext(ctx, `INSERT INTO bots (id, name, port, created_at, current_version)
		VALUES (1, 'test', 20001, ?, 'v1')`, store.Now()); err != nil {
		t.Fatal(err)
	}
	storeRelease(t, c, 1, "v2", config)
	if _, err := c.DB.W.ExecContext(ctx, `INSERT INTO canaries (bot_id, version, auto_after, started_at)
		VALUES (1, 'v2', 1, ?)`, store.FormatTime(time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
}
