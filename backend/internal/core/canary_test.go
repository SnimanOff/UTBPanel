package core

import (
	"context"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
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
