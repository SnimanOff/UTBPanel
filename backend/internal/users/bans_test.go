package users

import (
	"context"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
)

func TestBans(t *testing.T) {
	ctx := context.Background()
	tr := newTracker(t)
	b := &Bans{DB: tr.DB}
	if err := b.Ban(ctx, 1, 5, " ", 0, "home-pc"); err != ErrReason {
		t.Fatalf("no reason: %v", err)
	}
	if err := b.Ban(ctx, 1, 5, "спам", 0, "home-pc"); err != nil {
		t.Fatal(err)
	}
	msg := ingress.Parse(1, []byte(`{"update_id":1,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"text":"x"}}`), time.Now())
	if v := b.Check(ctx, msg); !v.Stop || !v.Silent {
		t.Fatalf("banned message: %+v", v)
	}
	pay := ingress.Parse(1, []byte(`{"update_id":2,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"successful_payment":{"total_amount":1}}}`), time.Now())
	if v := b.Check(ctx, pay); v.Stop {
		t.Fatal("payment of a banned user must reach the bot")
	}
	other := ingress.Parse(2, msg.Body, time.Now())
	if b.Check(ctx, other).Stop {
		t.Fatal("ban leaked into another bot")
	}
	// A restart of the core reads bans from core.db.
	b2 := &Bans{DB: tr.DB}
	b2.Load(ctx)
	if !b2.Banned(1, 5) {
		t.Fatal("ban lost")
	}
	b.Unban(ctx, 1, 5)
	if b.Banned(1, 5) {
		t.Fatal("unban")
	}
	// Temporary ban expires.
	b.Ban(ctx, 1, 6, "флуд", time.Millisecond, "антифлуд")
	time.Sleep(5 * time.Millisecond)
	if b.Banned(1, 6) {
		t.Fatal("expired ban still active")
	}
	if n, _ := b.Expire(ctx); n != 1 {
		t.Fatalf("expired = %d", n)
	}
}
