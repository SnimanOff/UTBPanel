package core

import (
	"context"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

func TestAutorollbackOnErrors(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	withTelegram(t, c)
	b, _ := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:good"})
	c.rules = deploy.Rules{Window: 5 * time.Second, ReadyAfter: time.Hour, MaxCrashes: 2, ErrShare: 0.2, MinUpdates: 5}
	var got []events.Event
	c.Ev.Subscribe(func(e events.Event) { got = append(got, e) })

	// v2 is current after a deploy from v1; a payment came while it ran.
	c.DB.W.Exec(`UPDATE bots SET current_version = 'v2', previous_version = 'v1' WHERE id = ?`, b.ID)
	res, _ := c.DB.W.Exec(`INSERT INTO deploys (bot_id, from_version, to_version, started_at) VALUES (?, 'v1', 'v2', ?)`, b.ID, store.Now())
	id, _ := res.LastInsertId()
	c.watchDeploy(id, b.ID, "v1", "v2", "", 0)
	c.DB.W.Exec(`INSERT INTO payments (bot_id, ts, user_id, currency, amount, charge_id) VALUES (?, ?, 5, 'XTR', 100, 'ch1')`, b.ID, store.Now())

	u := &ingress.Update{BotID: b.ID}
	c.observeOutcome(ingress.Outcome{Update: u, Result: "delivered"})
	for i := 0; i < 4; i++ {
		c.observeOutcome(ingress.Outcome{Update: u, Result: "timeout"})
	}
	// Another bot's errors do not count.
	c.observeOutcome(ingress.Outcome{Update: &ingress.Update{BotID: b.ID + 1}, Result: "error"})

	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if r := c.waitDeploy(wctx, id); r != "rolled_back" {
		t.Fatalf("result %q", r)
	}
	cur, prev, _ := c.versions(ctx, b.ID)
	if cur != "v1" || prev != "v2" {
		t.Fatalf("versions %s / %s", cur, prev)
	}
	ds, _ := c.Deploys(ctx, b.ID, 5)
	if ds[0].Result != "rolled_back" || ds[0].Error == "" {
		t.Fatalf("%+v", ds[0])
	}
	var ev *events.Event
	for i := range got {
		if got[i].Type == "autorollback" {
			ev = &got[i]
		}
	}
	if ev == nil {
		t.Fatal("no autorollback event")
	}
	if ps, _ := ev.Data["payments"].([]map[string]any); len(ps) != 1 || ps[0]["charge_id"] != "ch1" {
		t.Fatalf("payments in the event: %v", ev.Data["payments"])
	}
}

func TestObservationPasses(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	withTelegram(t, c)
	b, _ := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:good"})
	c.rules = deploy.Rules{Window: 2 * time.Second, ReadyAfter: time.Hour, MaxCrashes: 2, ErrShare: 0.2, MinUpdates: 5}
	c.DB.W.Exec(`UPDATE bots SET current_version = 'v2', previous_version = 'v1' WHERE id = ?`, b.ID)
	res, _ := c.DB.W.Exec(`INSERT INTO deploys (bot_id, from_version, to_version, started_at) VALUES (?, 'v1', 'v2', ?)`, b.ID, store.Now())
	id, _ := res.LastInsertId()
	c.watchDeploy(id, b.ID, "v1", "v2", "", 0)
	if r := c.waitDeploy(ctx, id); r != "ok" {
		t.Fatalf("result %q", r)
	}
	if cur, _, _ := c.versions(ctx, b.ID); cur != "v2" {
		t.Fatal("rolled back without a reason")
	}
	ds, _ := c.Deploys(ctx, b.ID, 5)
	if ds[0].Result != "ok" {
		t.Fatalf("%+v", ds[0])
	}
}
