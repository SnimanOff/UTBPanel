package alerts

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func TestFeed(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	s := &Service{DB: db, Now: func() time.Time { return now }, Name: func(int64) string { return "Echo" }}
	ch, cancel := s.Subscribe()
	defer cancel()

	crash := events.Event{Type: "bot_crashed", BotID: 1, Data: map[string]any{"exit": int64(2)}}
	s.Handle(crash)
	now = now.Add(time.Minute)
	s.Handle(crash) // a repeat: glued
	ns, _ := s.List(ctx, 0)
	if len(ns) != 1 || ns[0].Count != 2 || ns[0].Text != "Echo: упал (код 2)" {
		t.Fatalf("%+v", ns)
	}
	if len(ch) != 1 {
		t.Fatalf("pushed %d, a repeat is not pushed", len(ch))
	}
	if p, _ := s.Problems(ctx); len(p) != 1 || p[0] != 1 {
		t.Fatalf("problems %v", p)
	}
	// After the repeat window: a new row.
	now = now.Add(RepeatEvery + time.Minute)
	s.Handle(crash)
	// The bot runs again: problems closed, one "back to normal".
	s.Handle(events.Event{Type: "bot_started", BotID: 1})
	ns, _ = s.List(ctx, 0)
	if len(ns) != 3 || ns[0].Severity != "ok" || !ns[1].Resolved || !ns[2].Resolved {
		t.Fatalf("%+v", ns)
	}
	if p, _ := s.Problems(ctx); len(p) != 0 {
		t.Fatalf("problems after recovery %v", p)
	}
	// Reading clears the frame of a problem without recovery.
	s.Handle(events.Event{Type: "panic", BotID: 2, Data: map[string]any{"kind": "boom", "signature": "boom @ main.f"}})
	if p, _ := s.Problems(ctx); len(p) != 1 {
		t.Fatal("panic must mark the bot")
	}
	s.MarkRead(ctx, nil)
	if p, _ := s.Problems(ctx); len(p) != 0 {
		t.Fatal("read problems keep the frame")
	}
	if _, ok := Map(events.Event{Type: "bot_stopped"}); ok {
		t.Fatal("a stop by the owner is not a notification")
	}
}

func TestFullBackupEventsReachTheFeed(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Service{DB: db}
	for _, typ := range []string{"full_backup_failed", "full_backup_verification_failed", "full_backup_verification_skipped", "full_backup_download_reminder"} {
		s.Handle(events.Event{Type: typ, Data: map[string]any{"id": typ, "name": "archive.backup", "error": "damaged", "warning": "no space", "message": "Скачайте архив"}})
	}
	notes, err := s.List(ctx, 0)
	if err != nil || len(notes) != 4 {
		t.Fatal(notes, err)
	}
	for _, note := range notes {
		if note.Href != "/server" || note.Text == "" || note.BotID != 0 {
			t.Fatal(note)
		}
	}
}
