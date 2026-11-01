package users

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func newTracker(t *testing.T) *Tracker {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "core.db"), migrations.Core())
	if err != nil {
		t.Fatal(err)
	}
	w := store.NewWriter(db, time.Hour, nil)
	t.Cleanup(func() { w.Close(); db.Close() })
	return &Tracker{DB: db, W: w}
}

func upd(user int64, text string, at time.Time) *ingress.Update {
	body := `{"update_id":1,"message":{"chat":{"id":` + itoa(user) + `,"type":"private"},"from":{"id":` + itoa(user) + `,"username":"u` + itoa(user) + `","language_code":"ru"},"text":"` + text + `"}}`
	return ingress.Parse(1, []byte(body), at)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestAccountAndSource(t *testing.T) {
	ctx := context.Background()
	tr := newTracker(t)
	day := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	tr.Account(ctx, upd(5, "/start ads_vk", day))
	tr.Account(ctx, upd(5, "hi", day.Add(time.Minute)))
	if err := tr.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	// A later /start with another parameter does not change the source.
	tr.Account(ctx, upd(5, "/start other", day.Add(24*time.Hour)))
	tr.Flush(ctx)
	var source, last string
	var total int
	var bits []byte
	tr.DB.R.QueryRow(`SELECT source, last_seen, updates_total, active_days FROM bot_users WHERE user_id = 5`).Scan(&source, &last, &total, &bits)
	if source != "ads_vk" || total != 3 || CountActive(bits) != 2 {
		t.Fatalf("source=%s total=%d days=%d", source, total, CountActive(bits))
	}
	var lang string
	tr.DB.R.QueryRow(`SELECT language_code FROM tg_users WHERE user_id = 5`).Scan(&lang)
	if lang != "ru" {
		t.Fatalf("lang = %q", lang)
	}
}
