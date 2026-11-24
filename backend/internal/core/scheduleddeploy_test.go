package core

import (
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

func TestScheduledDeployAtMoscowMidnight(t *testing.T) {
	ctx := t.Context()
	c := newCore(t)
	if _, err := c.DB.W.Exec(`INSERT INTO bots (id, name, port, created_at, current_version, previous_version)
		VALUES (1, 'test', 20001, '', 'v1', 'v0')`); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"v0", "v1", "v2", "v3"} {
		storeRelease(t, c, 1, version, "")
	}
	settings := botconfig.DefaultReleases()
	settings.Keep = 2
	if err := c.SetSetting(ctx, 1, "releases", settings); err != nil {
		t.Fatal(err)
	}
	moscow := time.FixedZone("MSK", 3*3600)
	today := time.Now().In(moscow)
	midnight := time.Date(today.Year(), today.Month(), today.Day()+1, 0, 0, 0, 0, moscow)
	in := ScheduleDeploy{SwitchVersion: SwitchVersion{BotID: 1, Version: "v2"}, StartsAt: midnight}
	wireData, err := wire.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ScheduleDeploy
	if err := wire.Unmarshal(wireData, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := c.scheduleDeploy(ctx, decoded); err != nil {
		t.Fatal(err)
	}
	if err := c.scheduleDeploy(ctx, in); code(err) != "conflict" {
		t.Fatalf("replacing an active plan: %v", err)
	}
	plan, err := c.scheduledDeploy(ctx, 1)
	if err != nil || plan == nil || !plan.StartsAt.Equal(midnight) || plan.StartsAt.Hour() != 21 {
		t.Fatalf("scheduled deploy = %+v, %v", plan, err)
	}
	c.pruneReleases(ctx, 1)
	if !c.Releases.Exists(1, "v2") || c.Releases.Exists(1, "v3") {
		t.Fatal("release retention did not protect the scheduled version")
	}
	if err := c.tickScheduledDeploys(ctx, midnight.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if cur, _, _ := c.versions(ctx, 1); cur != "v1" {
		t.Fatalf("deployed too early: %s", cur)
	}
	for range 2 {
		if err := c.tickScheduledDeploys(ctx, midnight); err != nil {
			t.Fatal(err)
		}
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		plan, err = c.scheduledDeploy(ctx, 1)
		if err != nil || plan == nil || plan.Status != "running" {
			break
		}
	}
	if err != nil || plan == nil || plan.Status != "ok" {
		t.Fatalf("completed plan = %+v, %v", plan, err)
	}
	history, err := c.Deploys(ctx, 1, 10)
	if err != nil || len(history) != 1 || history[0].To != "v2" || history[0].Actor != "schedule:schedule" {
		t.Fatalf("deploy history = %+v, %v", history, err)
	}
}

func TestScheduledDeployRechecksConfigAndCancelsByID(t *testing.T) {
	ctx := t.Context()
	c := newCore(t)
	if _, err := c.DB.W.Exec(`INSERT INTO bots (id, name, port, created_at, current_version)
		VALUES (1, 'test', 20001, '', 'v1')`); err != nil {
		t.Fatal(err)
	}
	storeRelease(t, c, 1, "v2", "env: { LOG_LEVEL: debug }\n")
	in := ScheduleDeploy{SwitchVersion: SwitchVersion{BotID: 1, Version: "v2"}, StartsAt: time.Now().Add(time.Hour)}
	if err := c.scheduleDeploy(ctx, in); code(err) != "config_confirm" {
		t.Fatalf("scheduling without confirmation: %v", err)
	}
	snapshot, err := c.ConfigSnapshot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	in.ConfigVersion = snapshot.Version
	if err := c.scheduleDeploy(ctx, in); err != nil {
		t.Fatal(err)
	}
	if now, err := c.ConfigSnapshot(ctx, 1); err != nil || now.Version != snapshot.Version {
		t.Fatalf("scheduling changed settings: %+v, %v", now, err)
	}
	if err := c.SetSetting(ctx, 1, "tz", "Europe/Berlin"); err != nil {
		t.Fatal(err)
	}
	if err := c.tickScheduledDeploys(ctx, in.StartsAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	failed, err := c.scheduledDeploy(ctx, 1)
	if err != nil || failed == nil || failed.Status != "failed" || failed.Error == "" {
		t.Fatalf("stale confirmation = %+v, %v", failed, err)
	}
	snapshot, err = c.ConfigSnapshot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	in.ConfigVersion = snapshot.Version
	if err := c.scheduleDeploy(ctx, in); err != nil {
		t.Fatal(err)
	}
	cancel := CancelScheduledDeploy{BotRef: BotRef{BotID: 1}, ID: failed.ID}
	if err := c.cancelScheduledDeploy(ctx, cancel); code(err) != "conflict" {
		t.Fatalf("canceling an old plan: %v", err)
	}
	pending, err := c.scheduledDeploy(ctx, 1)
	if err != nil || pending == nil || pending.ID == failed.ID {
		t.Fatalf("replacement plan = %+v, %v", pending, err)
	}
	cancel.ID = pending.ID
	if err := c.cancelScheduledDeploy(ctx, cancel); err != nil {
		t.Fatal(err)
	}
	if err := c.startScheduledDeploy(ctx, pending.ID); err != nil {
		t.Fatal(err)
	}
	if plan, err := c.scheduledDeploy(ctx, 1); err != nil || plan != nil {
		t.Fatalf("canceled plan = %+v, %v", plan, err)
	}
	if cur, _, _ := c.versions(ctx, 1); cur != "v1" {
		t.Fatalf("bot switched after stale or canceled confirmation: %s", cur)
	}
}
