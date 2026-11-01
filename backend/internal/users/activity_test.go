package users

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestMarkAndShift(t *testing.T) {
	var bits []byte
	var anchor int64
	bits, anchor = Mark(bits, anchor, 1000)
	bits, anchor = Mark(bits, anchor, 1001)
	bits, anchor = Mark(bits, anchor, 1005)
	if anchor != 1005 || CountActive(bits) != 3 {
		t.Fatalf("anchor=%d count=%d", anchor, CountActive(bits))
	}
	for _, d := range []int64{1000, 1001, 1005} {
		if !Active(bits, anchor, d) {
			t.Errorf("day %d lost", d)
		}
	}
	if Active(bits, anchor, 1002) {
		t.Error("day 1002 set")
	}
	// An older day inside the window is still recorded.
	bits, anchor = Mark(bits, anchor, 1003)
	if !Active(bits, anchor, 1003) || anchor != 1005 {
		t.Fatal("older day")
	}
	// A break longer than 128 days wipes the window.
	bits, anchor = Mark(bits, anchor, 1005+200)
	if CountActive(bits) != 1 || Active(bits, anchor, 1005) {
		t.Fatalf("after a long break: %d", CountActive(bits))
	}
	// A day older than the window is ignored.
	bits, _ = Mark(bits, anchor, anchor-Days)
	if CountActive(bits) != 1 {
		t.Fatal("day outside the window recorded")
	}
}

func TestRetention(t *testing.T) {
	ctx := context.Background()
	tr := newTracker(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	start := now.Add(-20 * 24 * time.Hour)
	// Four users came 20 days ago; 1 and 2 returned the next day, 1 also on day 7.
	for u := int64(1); u <= 4; u++ {
		tr.Account(ctx, upd(u, "/start", start))
	}
	tr.Account(ctx, upd(1, "x", start.Add(24*time.Hour)))
	tr.Account(ctx, upd(2, "x", start.Add(24*time.Hour)))
	tr.Account(ctx, upd(1, "x", start.Add(7*24*time.Hour)))
	if err := tr.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if r, n, _ := tr.Retention(ctx, 1, 1, now); n != 4 || r != 0.5 {
		t.Fatalf("D1 = %v of %d", r, n)
	}
	if r, _, _ := tr.Retention(ctx, 1, 7, now); r != 0.25 {
		t.Fatalf("D7 = %v", r)
	}
	if _, n, _ := tr.Retention(ctx, 1, 30, now); n != 0 {
		t.Fatalf("D30 cohort must be empty, got %d", n)
	}
}

func TestParallelAccounting(t *testing.T) {
	ctx := context.Background()
	tr := newTracker(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				tr.Flush(ctx)
			}
		}
	}()
	for g := range 8 {
		wg.Go(func() {
			for i := range 500 {
				tr.Account(ctx, upd(int64(g*1000+i%50+1), "x", now))
			}
		})
	}
	wg.Wait()
	close(stop)
	if err := tr.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	var users, total int
	tr.DB.R.QueryRow(`SELECT COUNT(*), SUM(updates_total) FROM bot_users`).Scan(&users, &total)
	if users != 400 || total != 4000 {
		t.Fatalf("users=%d updates=%d", users, total)
	}
}
