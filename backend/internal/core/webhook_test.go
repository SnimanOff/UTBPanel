package core

import (
	"context"
	"strings"
	"testing"
)

func TestWebhookHijack(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	c.Cfg.PublicAddr = "203.0.113.10"
	f := withTelegram(t, c)
	b, _ := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:good"})
	if err := c.SetWebhook(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.hookURL = c.WebhookURL(b.ID)
	f.mu.Unlock()
	if st, err := c.CheckWebhook(ctx, b.ID); err != nil || st.HijackedURL != "" {
		t.Fatalf("%+v %v", st, err)
	}
	countSet := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, call := range f.calls {
			if strings.HasPrefix(call, "setWebhook") {
				n++
			}
		}
		return n
	}
	// A debug copy took the webhook: fixed once.
	f.mu.Lock()
	f.hookURL = "https://laptop.example/hook"
	f.mu.Unlock()
	st, _ := c.CheckWebhook(ctx, b.ID)
	if st.HijackedURL != "https://laptop.example/hook" || countSet() != 2 {
		t.Fatalf("first hijack: %+v sets=%d", st, countSet())
	}
	// Again: only an event, no fight.
	f.mu.Lock()
	f.hookURL = "https://laptop.example/hook"
	f.mu.Unlock()
	c.CheckWebhook(ctx, b.ID)
	if countSet() != 2 {
		t.Fatalf("second hijack fixed again: sets=%d", countSet())
	}
	evs, _ := c.Ev.Since(ctx, 0, 100)
	n := 0
	for _, e := range evs {
		if e.Type == "webhook_hijacked" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("hijack events = %d", n)
	}
}
