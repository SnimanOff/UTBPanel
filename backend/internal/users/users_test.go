package users

import (
	"context"
	"database/sql"
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

func TestBlocksAndPayments(t *testing.T) {
	ctx := context.Background()
	tr := newTracker(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	member := func(status string, at time.Time) *ingress.Update {
		return ingress.Parse(1, []byte(`{"update_id":2,"my_chat_member":{"chat":{"id":5,"type":"private"},"from":{"id":5},"new_chat_member":{"status":"`+status+`"}}}`), at)
	}
	var blocks []bool
	tr.OnBlock = func(_, _ int64, b bool) { blocks = append(blocks, b) }
	tr.Account(ctx, upd(5, "hi", now))
	tr.Account(ctx, member("kicked", now.Add(time.Second)))
	tr.Flush(ctx)
	var blocked sql.NullString
	tr.DB.R.QueryRow(`SELECT blocked_at FROM bot_users WHERE user_id = 5`).Scan(&blocked)
	if !blocked.Valid {
		t.Fatal("block not stored")
	}
	tr.Account(ctx, member("member", now.Add(time.Minute)))
	tr.Flush(ctx)
	tr.DB.R.QueryRow(`SELECT blocked_at FROM bot_users WHERE user_id = 5`).Scan(&blocked)
	if blocked.Valid || len(blocks) != 2 || !blocks[0] || blocks[1] {
		t.Fatalf("unblock: %v %v", blocked, blocks)
	}

	pay := func(kind string) *ingress.Update {
		return ingress.Parse(1, []byte(`{"update_id":3,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"`+kind+`":{"currency":"XTR","total_amount":50,"invoice_payload":"p1","telegram_payment_charge_id":"ch1"}}}`), now)
	}
	tr.Payments(ctx, pay("successful_payment"))
	tr.Payments(ctx, pay("successful_payment")) // Telegram retried: no duplicate
	tr.Payments(ctx, pay("refunded_payment"))
	var n int
	var refunded sql.NullString
	tr.DB.R.QueryRow(`SELECT COUNT(*), MAX(refunded_at) FROM payments`).Scan(&n, &refunded)
	if n != 1 || !refunded.Valid {
		t.Fatalf("payments = %d refunded=%v", n, refunded)
	}
}

func TestListAndAudience(t *testing.T) {
	ctx := context.Background()
	tr := newTracker(t)
	now := time.Now()
	tr.Account(ctx, upd(5, "/start ads", now))
	tr.Account(ctx, upd(6, "/start ads", now))
	tr.Account(ctx, upd(7, "hi", now))
	tr.Flush(ctx)
	res, err := tr.List(ctx, ListQuery{BotID: 1, Query: "@u6"})
	if err != nil || res.Total != 1 || res.Users[0].ID != 6 || res.Users[0].Source != "ads" {
		t.Fatalf("%+v %v", res, err)
	}
	if res, _ := tr.List(ctx, ListQuery{BotID: 1}); res.Total != 3 {
		t.Fatalf("all = %d", res.Total)
	}
	a, err := tr.Audience(ctx, 1)
	if err != nil || a.Total != 3 || len(a.Sources) != 1 || a.Sources[0].Count != 2 || a.Languages[0].Key != "ru" {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestAudienceActivity(t *testing.T) {
	ctx := context.Background()
	tr := newTracker(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	activityDays := [][]int{
		{29, 1, 0},   // regular: oldest day in the window counts
		{30, 1, 0},   // recent: oldest day is outside the window
		{29, 29, 29}, // recent: several updates on the same day
		{30, 30},     // one active day before the window
		{200, 31},    // previously active, even though only one day is left in the bitmap
		{2, 1, 0},    // currently blocked
	}
	for i, days := range activityDays {
		for _, ago := range days {
			u := upd(int64(i+1), "hi", now.AddDate(0, 0, -ago))
			u.Premium = i == 0
			tr.Account(ctx, u)
		}
	}
	otherBot := upd(8, "hi", now)
	otherBot.BotID = 2
	tr.Account(ctx, otherBot)
	if err := tr.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.DB.W.Exec(`UPDATE bot_users SET blocked_at = ? WHERE bot_id = 1 AND user_id = 6`, store.FormatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.DB.W.Exec(`INSERT INTO bot_users (bot_id, user_id, first_seen, last_seen, banned) VALUES (1, 7, ?, ?, 1)`,
		store.FormatTime(now), store.FormatTime(now)); err != nil {
		t.Fatal(err)
	}

	var a Audience
	if err := tr.audienceActivity(ctx, 1, now, &a); err != nil {
		t.Fatal(err)
	}
	want := AudienceActivity{Regular: 1, Recent: 2, OneDay: 1, Inactive: 1, Blocked: 1, Untracked: 1}
	if a.Total != 7 || a.Premium != 1 || *a.Activity != want {
		t.Fatalf("total=%d premium=%d activity=%+v, want total=7 premium=1 activity=%+v", a.Total, a.Premium, *a.Activity, want)
	}

	var empty Audience
	if err := tr.audienceActivity(ctx, 3, now, &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Total != 0 || empty.Premium != 0 || *empty.Activity != (AudienceActivity{}) {
		t.Fatalf("empty bot has audience: %+v, activity: %+v", empty, *empty.Activity)
	}
}
