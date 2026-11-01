package alerts

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/metrics"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func TestRules(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	w := store.NewWriter(db, time.Hour, nil)
	defer func() { w.Close(); db.Close() }()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	ms := &metrics.Storage{DB: db, W: w}
	notes := &Service{DB: db, Now: clock}
	rs := &Rules{S: ms, Notes: notes, Now: clock, Bots: func(context.Context) []int64 { return []int64{1} }}

	minute := func(at time.Time, updates, errs int64) metrics.Minute {
		return metrics.Minute{BotID: 1, TS: at.Unix(), Counters: map[string]int64{metrics.Updates: updates, metrics.Errors: errs}}
	}
	var batch []metrics.Minute
	for i := 1; i <= 60; i++ {
		batch = append(batch, minute(now.Add(-time.Duration(i)*time.Minute), 20, 0))
	}
	ms.Save(ctx, batch)

	if _, err := rs.Save(ctx, Rule{BotID: 0, Metric: "nope", Value: 1, Minutes: 5}); err == nil {
		t.Fatal("unknown metric accepted")
	}
	errRule, _ := rs.Save(ctx, Rule{BotID: 1, Metric: "err_rate", Op: ">", Value: 5, Minutes: 5, Enabled: true})
	rs.Save(ctx, Rule{BotID: 0, Metric: "silence", Minutes: 10, Enabled: true})
	rs.Check(ctx)
	if ns, _ := notes.List(ctx, 0); len(ns) != 0 {
		t.Fatalf("fired on a healthy bot: %+v", ns)
	}

	// 3 minutes with 50 % errors, then 10 minutes of silence.
	now = now.Add(3 * time.Minute)
	ms.Save(ctx, []metrics.Minute{minute(now.Add(-3*time.Minute), 20, 10), minute(now.Add(-2*time.Minute), 20, 10), minute(now.Add(-time.Minute), 20, 10)})
	rs.Check(ctx)
	ns, _ := notes.List(ctx, 0)
	if len(ns) != 1 || ns[0].Severity != "error" {
		t.Fatalf("%+v", ns)
	}
	rs.Check(ctx) // still firing: no second notification
	if ns, _ := notes.List(ctx, 0); len(ns) != 1 {
		t.Fatal("fired twice")
	}
	now = now.Add(11 * time.Minute)
	rs.Check(ctx)
	ns, _ = notes.List(ctx, 0)
	kinds := map[string]int{}
	for _, n := range ns {
		kinds[n.Severity]++
	}
	// No updates for 10 minutes: silence fires (the error rule has too few
	// updates to judge and stays as it was).
	if kinds["error"] < 2 {
		t.Fatalf("silence must fire: %+v", ns)
	}
	if err := rs.Delete(ctx, errRule.ID); err != nil {
		t.Fatal(err)
	}
}
