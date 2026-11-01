package metrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func minute(ts int64, updates, errs int64, ms int, crashes int64) Minute {
	h := NewHist()
	for range updates {
		h.Add(time.Duration(ms) * time.Millisecond)
	}
	return Minute{BotID: 1, TS: ts,
		Counters: map[string]int64{Updates: updates, Errors: errs, Crashes: crashes},
		Hists:    map[string]Hist{RespTime: h},
		Gauges:   map[string]float64{CPU: 10, RSS: 50e6},
	}
}

func TestDeployReport(t *testing.T) {
	ctx := context.Background()
	db, _ := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	w := store.NewWriter(db, time.Hour, nil)
	defer func() { w.Close(); db.Close() }()
	s := &Storage{DB: db, W: w}
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var ms []Minute
	for i := int64(1); i <= 60; i++ {
		ms = append(ms, minute(at.Unix()-i*60, 10, 0, 20, 0))      // before: fast, clean
		ms = append(ms, minute(at.Unix()+(i-1)*60, 10, 1, 120, 0)) // after: slow, 10 % errors
	}
	ms[len(ms)-1].Counters[Crashes] = 1
	if err := s.Save(ctx, ms); err != nil {
		t.Fatal(err)
	}
	s.AddDeploy(ctx, 1, at, "v2", "ok")
	var emitted []string
	emit := func(_ int64, data map[string]any) { emitted = append(emitted, data["version"].(string)) }

	// Not an hour yet.
	s.MakeReports(ctx, at.Add(30*time.Minute), emit)
	if rs, _ := s.Reports(ctx, 1); len(rs) != 0 {
		t.Fatal("report before the hour passed")
	}
	if err := s.MakeReports(ctx, at.Add(61*time.Minute), emit); err != nil {
		t.Fatal(err)
	}
	rs, _ := s.Reports(ctx, 1)
	if len(rs) != 1 {
		t.Fatalf("reports %v", rs)
	}
	r := rs[0]
	if r.Before.Updates != 600 || r.After.Errors != 60 || r.After.Crashes != 1 || r.Before.Minutes != 60 {
		t.Fatalf("%+v", r)
	}
	if len(r.Worse) != 3 || len(emitted) != 1 {
		t.Fatalf("worse %v, events %v", r.Worse, emitted)
	}
	// Made once.
	s.MakeReports(ctx, at.Add(2*time.Hour), emit)
	if len(emitted) != 1 {
		t.Fatal("report made twice")
	}
}

func TestWorseIgnoresSmallNumbers(t *testing.T) {
	before := Summary{Updates: 3, P95: 10}
	after := Summary{Updates: 3, P95: 40, Errors: 1, ErrRate: 0.33}
	if w := Worse(before, after); len(w) != 0 {
		t.Fatalf("%v", w)
	}
}
