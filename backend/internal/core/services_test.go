package core

import (
	"context"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/tgproxy"
)

func TestServiceDownAndBack(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	var first []string
	c.svc.onFirst = func(_ int64, h string) { first = append(first, h) }
	for i := range 10 {
		cn := tgproxy.Conn{BotID: 1 + int64(i%2), Host: "api.pay.example:443"}
		if i < 5 {
			cn.Err, cn.Done = "timeout", true
		}
		c.onConn(cn)
	}
	c.checkServices(time.Now())
	evs, _ := c.Ev.Since(ctx, 0, 10)
	if len(evs) != 1 || evs[0].Type != "service_down" {
		t.Fatalf("events = %+v", evs)
	}
	if len(first) != 2 {
		t.Fatalf("first seen = %v (once per bot)", first)
	}
	// Nothing new within the window: still down, no new event.
	c.checkServices(time.Now())
	// The window passes with good connections only.
	c.checkServices(time.Now().Add(ServiceWindow + time.Minute))
	evs, _ = c.Ev.Since(ctx, 0, 10)
	if len(evs) != 2 || evs[1].Type != "service_recovered" {
		t.Fatalf("events = %+v", evs)
	}
}

func TestServiceLimit(t *testing.T) {
	c := newCore(t)
	old := MaxServices
	MaxServices = 2
	t.Cleanup(func() { MaxServices = old })
	c.svc.mu.Lock()
	names := []string{c.svc.name(1, "a:443"), c.svc.name(1, "b:443"), c.svc.name(1, "c:443"), c.svc.name(1, "a:443")}
	c.svc.mu.Unlock()
	if names[2] != "other" || names[3] != "a:443" {
		t.Fatalf("%v", names)
	}
}
