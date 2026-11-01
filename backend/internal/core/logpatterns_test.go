package core

import (
	"context"
	"testing"
)

func TestLogPatternsSetting(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	withTelegram(t, c)
	b, _ := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:good"})
	if err := c.PutSetting(ctx, SettingPut{BotID: b.ID, Key: "log_patterns", Value: `[{"name":"Bad Name","pattern":"x"}]`}); err == nil {
		t.Fatal("bad name accepted")
	}
	var counted []string
	c.patterns.Count = func(_ int64, name string) { counted = append(counted, name) }
	if err := c.PutSetting(ctx, SettingPut{BotID: b.ID, Key: "log_patterns", Value: `[{"name":"orders","pattern":"order created"}]`}); err != nil {
		t.Fatal(err)
	}
	c.patterns.Line(b.ID, "INFO order created id=1")
	if len(counted) != 1 || counted[0] != "log_orders" {
		t.Fatalf("%v", counted)
	}
}
