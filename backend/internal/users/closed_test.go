package users

import (
	"context"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
)

func TestClosedMode(t *testing.T) {
	ctx := context.Background()
	tr := newTracker(t)
	tr.DB.W.Exec(`INSERT INTO bots (id, name, port, created_at) VALUES (1, 'shop', 20000, 'x')`)
	tr.Account(ctx, upd(7, "hi", time.Now()))
	tr.Flush(ctx)
	c := &Closed{DB: tr.DB.R, W: tr.DB.W}
	msg := func(user int64) *ingress.Update { return upd(user, "hi", time.Now()) }
	if c.Check(ctx, msg(5)).Stop {
		t.Fatal("closed mode is off by default")
	}
	c.Set(ctx, 1, true, "Бот в разработке")
	v := c.Check(ctx, msg(5))
	if !v.Stop || v.Text != "Бот в разработке" {
		t.Fatalf("%+v", v)
	}
	id, err := c.Resolve(ctx, 1, "@U7")
	if err != nil || id != 7 {
		t.Fatalf("resolve: %d %v", id, err)
	}
	if _, err := c.Resolve(ctx, 1, "@nobody"); err != ErrUnknownUser {
		t.Fatalf("unknown: %v", err)
	}
	c.Allow(ctx, 1, 7, "я")
	if c.Check(ctx, msg(7)).Stop {
		t.Fatal("allowed user stopped")
	}
	st, _ := c.State(ctx, 1)
	if !st.Enabled || len(st.Allow) != 1 || st.Allow[0].Username != "u7" {
		t.Fatalf("%+v", st)
	}
	c.Remove(ctx, 1, 7)
	if !c.Check(ctx, msg(7)).Stop {
		t.Fatal("removed user still allowed")
	}
}
