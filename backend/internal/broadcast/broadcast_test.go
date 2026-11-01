package broadcast

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
)

// fakeTG answers by chat id: 403 for blocked, 400 for missing chats.
type fakeTG struct {
	mu      sync.Mutex
	blocked map[int64]bool
	missing map[int64]bool
	sent    []int64
	photos  []string // photo field per sendPhoto: "" means an upload
	hook    func(chat int64) error
}

func (f *fakeTG) answer(chat int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hook != nil {
		if err := f.hook(chat); err != nil {
			return err
		}
	}
	if f.blocked[chat] {
		return &telegram.Error{Code: 403, Description: "Forbidden: bot was blocked by the user"}
	}
	if f.missing[chat] {
		return &telegram.Error{Code: 400, Description: "Bad Request: chat not found"}
	}
	f.sent = append(f.sent, chat)
	return nil
}

func (f *fakeTG) SendMessage(_ context.Context, _ string, p telegram.SendMessage) (telegram.Message, error) {
	return telegram.Message{}, f.answer(p.ChatID)
}

func (f *fakeTG) SendPhoto(_ context.Context, _ string, p telegram.SendPhoto) (telegram.Message, error) {
	f.mu.Lock()
	f.photos = append(f.photos, p.Photo)
	f.mu.Unlock()
	m := telegram.Message{}
	m.Photo = append(m.Photo, struct {
		FileID string `json:"file_id"`
	}{FileID: "small"}, struct {
		FileID string `json:"file_id"`
	}{FileID: "big"})
	return m, f.answer(p.ChatID)
}

func newService(t *testing.T, tg *fakeTG, users int) *Service {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "core.db"), migrations.Core())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := store.Now()
	for i := 1; i <= users; i++ {
		if _, err := db.W.Exec(`INSERT INTO bot_users (bot_id, user_id, first_seen, last_seen) VALUES (1, ?, ?, ?)`, i, now, now); err != nil {
			t.Fatal(err)
		}
	}
	return &Service{DB: db, TG: tg, Limit: NewLimiter(), SaveEvery: 3,
		Token: func(context.Context, int64) (string, error) { return "1:tok", nil }}
}

func TestSendAll(t *testing.T) {
	ctx := context.Background()
	tg := &fakeTG{blocked: map[int64]bool{2: true}, missing: map[int64]bool{3: true}}
	s := newService(t, tg, 6)
	s.DB.W.Exec(`UPDATE bot_users SET banned = 1 WHERE user_id = 6`)
	b, err := s.Create(ctx, B{BotID: 1, Text: "<b>Hi</b>", Photo: []byte("jpeg"), Buttons: []Button{{Text: "Site", URL: "https://x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Estimate(ctx, 1, "all", 0); n != 5 {
		t.Fatalf("estimate %d, banned must be excluded", n)
	}
	s.Tick(ctx)
	s.Wait()
	got, _ := s.Get(ctx, b.ID)
	if got.State != Done || got.Sent != 3 || got.Blocked != 1 || got.Failed != 1 || got.Total != 5 {
		t.Fatalf("%+v", got)
	}
	if got.Errors["Bad Request: chat not found"] != 1 {
		t.Fatalf("errors %v", got.Errors)
	}
	// The picture is uploaded once, then sent by the largest file_id.
	if tg.photos[0] != "" || tg.photos[len(tg.photos)-1] != "big" {
		t.Fatalf("photos %v", tg.photos)
	}
	var blockedAt *string
	s.DB.R.QueryRow(`SELECT blocked_at FROM bot_users WHERE user_id = 2`).Scan(&blockedAt)
	if blockedAt == nil {
		t.Fatal("403 must mark the user as blocked")
	}
	if n, _ := s.Estimate(ctx, 1, "all", 0); n != 4 {
		t.Fatal("blocked user still in the audience")
	}
}

func TestLimiterLeavesRoomForBot(t *testing.T) {
	l := NewLimiter()
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 10; i++ {
		l.Note(1)
	}
	n := 0
	for l.reserve(1) == 0 {
		n++
	}
	if n != 18 {
		t.Fatalf("broadcast got %d of 28 with 10 own calls", n)
	}
	now = now.Add(1100 * time.Millisecond)
	n = 0
	for l.reserve(1) == 0 {
		n++
	}
	if n != 25 {
		t.Fatalf("quiet second: %d, want the share of 25", n)
	}
	l.Pause(1, 5*time.Second)
	now = now.Add(1100 * time.Millisecond)
	if d := l.reserve(1); d < 3*time.Second {
		t.Fatalf("pause after 429: wait %v", d)
	}
}
