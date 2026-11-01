package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/maintenance"
)

func TestPauseAnswersAndDefers(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	withTelegram(t, c)
	b, _ := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:good"})
	if err := c.Maint.PauseBot(ctx, b.ID, ""); err != nil {
		t.Fatal(err)
	}
	msg := `{"update_id":1,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"text":"hi"}}`
	rep := c.Pipe.Handle(ctx, b.ID, []byte(msg), time.Now())
	if !strings.Contains(string(rep.Body), maintenance.DefaultText) {
		t.Fatalf("answer = %s", rep.Body)
	}
	pay := `{"update_id":2,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"successful_payment":{"total_amount":1}}}`
	c.Pipe.Handle(ctx, b.ID, []byte(pay), time.Now())
	if c.DeferredCount(ctx, b.ID) != 1 {
		t.Fatal("payment not deferred during pause")
	}
	bot, _ := c.Bot(ctx, b.ID)
	if bot.Maintenance != "пауза" {
		t.Fatalf("label = %q", bot.Maintenance)
	}
	doc, _ := c.EdgeSettings(ctx)
	if !doc.Bots[b.ID].Maintenance || doc.Bots[b.ID].TZ != "Europe/Moscow" {
		t.Fatalf("edge settings = %+v", doc.Bots[b.ID])
	}
	// A restart of the core keeps the pause.
	m2 := c.newMaintenance()
	m2.Load(ctx)
	if !m2.IsActive(ctx, b.ID) {
		t.Fatal("pause lost after reload")
	}
	c.Maint.End(ctx, b.ID)
	if c.Maint.IsActive(ctx, b.ID) {
		t.Fatal("still paused")
	}
}

func TestRender(t *testing.T) {
	msk, _ := time.LoadLocation("Europe/Moscow")
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	start := time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 2, 16, 0, 0, 0, time.UTC)
	got := maintenance.Render("с {start} до {end}", start, end, msk, now)
	if got != "с 18:00 до 02.11 19:00" {
		t.Fatalf("%q", got)
	}
}
