package users

import (
	"context"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
)

func TestAntiflood(t *testing.T) {
	ctx := context.Background()
	tr := newTracker(t)
	bans := &Bans{DB: tr.DB}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	a := &Antiflood{DB: tr.DB.R, Bans: bans, Now: func() time.Time { return now }}
	msg := upd(5, "x", now)
	pay := ingress.Parse(1, []byte(`{"update_id":2,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"successful_payment":{"total_amount":1}}}`), now)

	flood := func() (stopped, warned int) {
		for range 30 {
			v := a.Check(ctx, msg)
			if v.Stop {
				stopped++
				if v.Text != "" {
					warned++
				}
			}
		}
		return
	}
	stopped, warned := flood()
	if stopped != 10 || warned != 1 {
		t.Fatalf("stopped=%d warned=%d", stopped, warned)
	}
	if a.Check(ctx, pay).Stop {
		t.Fatal("payment counted by antiflood")
	}
	// A new window: the user may write again.
	now = now.Add(11 * time.Second)
	if a.Check(ctx, msg).Stop {
		t.Fatal("new window still blocked")
	}
	// Third violation within the hour: automatic ban.
	now = now.Add(11 * time.Second)
	flood()
	now = now.Add(11 * time.Second)
	flood()
	if !bans.Banned(1, 5) {
		t.Fatal("no automatic ban after 3 violations")
	}
	var by, reason string
	tr.DB.R.QueryRow(`SELECT banned_by, ban_reason FROM bot_users WHERE user_id = 5`).Scan(&by, &reason)
	if by != "антифлуд" || reason != "флуд" {
		t.Fatalf("ban by %q reason %q", by, reason)
	}
}
