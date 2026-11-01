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

func TestRetryAfter429(t *testing.T) {
	ctx := context.Background()
	tries := 0
	tg := &fakeTG{}
	tg.hook = func(chat int64) error {
		if chat == 2 && tries == 0 {
			tries++
			return &telegram.Error{Code: 429, Description: "Too Many Requests: retry after 1", RetryAfter: 1}
		}
		return nil
	}
	s := newService(t, tg, 3)
	b, _ := s.Create(ctx, B{BotID: 1, Text: "hi"})
	start := time.Now()
	s.Tick(ctx)
	s.Wait()
	got, _ := s.Get(ctx, b.ID)
	if got.Sent != 3 || got.Failed != 0 {
		t.Fatalf("%+v", got)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Fatal("429 must wait retry_after before the next message")
	}
	if len(tg.sent) != 3 || tg.sent[1] != 2 {
		t.Fatalf("order %v: the user that got 429 is retried", tg.sent)
	}
}

func TestContinueAfterRestart(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	tg := &fakeTG{}
	s := newService(t, tg, 10)
	tg.hook = func(chat int64) error {
		if chat == 4 {
			stop() // the core goes down while user 4 is being sent to
		}
		return nil
	}
	b, _ := s.Create(context.Background(), B{BotID: 1, Text: "hi"})
	s.Tick(ctx)
	s.Wait()
	mid, _ := s.Get(context.Background(), b.ID)
	if mid.State != Running || mid.Sent != 4 {
		t.Fatalf("after stop: %+v", mid)
	}

	// A new core on the same database continues from the cursor.
	tg.hook = nil
	s2 := &Service{DB: s.DB, TG: tg, Limit: NewLimiter(), Token: s.Token}
	s2.Tick(context.Background())
	s2.Wait()
	got, _ := s2.Get(context.Background(), b.ID)
	if got.State != Done || got.Sent != 10 {
		t.Fatalf("%+v", got)
	}
	seen := map[int64]int{}
	for _, c := range tg.sent {
		seen[c]++
	}
	for c, n := range seen {
		if n != 1 {
			t.Fatalf("user %d got the message %d times", c, n)
		}
	}
}

func TestPauseResumeCancel(t *testing.T) {
	ctx := context.Background()
	tg := &fakeTG{}
	s := newService(t, tg, 5)
	started := make(chan struct{})
	release := make(chan struct{})
	tg.hook = func(chat int64) error {
		if chat == 2 {
			close(started)
			tg.mu.Unlock()
			<-release
			tg.mu.Lock()
		}
		return nil
	}
	b, _ := s.Create(ctx, B{BotID: 1, Text: "hi"})
	s.Tick(ctx)
	<-started
	if err := s.Pause(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	s.Wait()
	p, _ := s.Get(ctx, b.ID)
	if p.State != Paused || p.Sent != 2 {
		t.Fatalf("paused: %+v", p)
	}
	s.Tick(ctx)
	s.Wait()
	if p2, _ := s.Get(ctx, b.ID); p2.Sent != 2 {
		t.Fatal("a paused broadcast must not send")
	}
	tg.hook = nil
	if err := s.Cancel(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume(ctx, b.ID); err != ErrState {
		t.Fatalf("resume after cancel: %v", err)
	}
	c, _ := s.Get(ctx, b.ID)
	if c.State != Cancelled || c.FinishedAt.IsZero() {
		t.Fatalf("%+v", c)
	}
}

func TestBotCallsGoFirst(t *testing.T) {
	ctx := context.Background()
	tg := &fakeTG{}
	s := newService(t, tg, 3)
	s.Limit = &Limiter{Total: 4, Share: 4}
	// The bot makes 10 calls a second for 600 ms: the whole budget is its.
	for i := 0; i < 4; i++ {
		s.Limit.Note(1)
	}
	busy := time.Now().Add(600 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for time.Now().Before(busy) {
			s.Limit.Note(1)
			time.Sleep(100 * time.Millisecond)
		}
	}()
	time.Sleep(50 * time.Millisecond)
	b, _ := s.Create(ctx, B{BotID: 1, Text: "hi"})
	var first time.Time
	tg.hook = func(int64) error {
		if first.IsZero() {
			first = time.Now()
		}
		return nil
	}
	s.Tick(ctx)
	s.Wait()
	<-done
	if first.Before(busy) {
		t.Fatalf("broadcast sent %v before the bot went quiet", busy.Sub(first))
	}
	if got, _ := s.Get(ctx, b.ID); got.Sent != 3 {
		t.Fatalf("%+v", got)
	}
}
