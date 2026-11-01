package core

import (
	"context"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
)

func TestCrashUpdates(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	withTelegram(t, c)
	b, _ := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:good"})
	upd := func(n int, failure string) ingress.Outcome {
		u := &ingress.Update{BotID: b.ID, Body: []byte(`{"update_id":` + string(rune('0'+n%10)) + `}`), Received: time.Now(), Failure: failure}
		return ingress.Outcome{Update: u, Result: "error", Status: 502}
	}
	c.keepCrashUpdate(upd(1, "crash"))
	if list, _ := c.CrashUpdates(ctx, b.ID); len(list) != 0 {
		t.Fatal("kept while the setting is off")
	}
	if err := c.PutSetting(ctx, SettingPut{BotID: b.ID, Key: "crash_updates", Value: "true"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		c.keepCrashUpdate(upd(i, "crash"))
	}
	c.keepCrashUpdate(ingress.Outcome{Update: &ingress.Update{BotID: b.ID, Body: []byte(`{}`)}, Result: "delivered", Status: 200})
	list, _ := c.CrashUpdates(ctx, b.ID)
	if len(list) != 20 || list[0].Reason != "crash" {
		t.Fatalf("kept %d", len(list))
	}
	c.PutSetting(ctx, SettingPut{BotID: b.ID, Key: "crash_updates", Value: "false"})
	if list, _ := c.CrashUpdates(ctx, b.ID); len(list) != 0 {
		t.Fatal("turning it off must drop the kept updates")
	}
}
