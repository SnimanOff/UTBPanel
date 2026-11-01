package metrics

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func sample(ts int64, n int64) Minute {
	h := NewHist()
	h.Add(time.Duration(n) * time.Millisecond)
	return Minute{BotID: 1, TS: ts,
		Counters: map[string]int64{Updates: n, "pay_XTR_sum": -5},
		Hists:    map[string]Hist{RespTime: h},
		Gauges:   map[string]float64{RSS: float64(n) * 1.5},
	}
}

func TestCodecRoundTrip(t *testing.T) {
	m := sample(60, 42)
	b := Encode(&m)
	var out Minute
	if err := Decode(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.Counters, m.Counters) || !reflect.DeepEqual(out.Gauges, m.Gauges) || out.Hists[RespTime].Count() != 1 {
		t.Fatalf("%+v", out)
	}
	if len(b) > 60 {
		t.Errorf("record is %d bytes", len(b))
	}
	if Decode(b[:len(b)-3], &out) == nil {
		t.Error("cut record decoded")
	}
}

func TestSaveAndRollup(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	w := store.NewWriter(db, time.Hour, nil)
	defer func() { w.Close(); db.Close() }()
	s := &Storage{DB: db, W: w}
	hour := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	var ms []Minute
	var want int64
	for i := range int64(60) {
		ms = append(ms, sample(hour.Unix()+i*60, i+1))
		want += i + 1
	}
	if err := s.Save(ctx, ms); err != nil {
		t.Fatal(err)
	}
	// The same minute again (refetch after a panel crash) merges.
	if err := s.Save(ctx, []Minute{sample(hour.Unix(), 0)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(ctx, hour.Add(70*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var data []byte
	if err := db.R.QueryRow(`SELECT data FROM m_hour WHERE bot_id = 1 AND ts = ?`, hour.Unix()).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var h Minute
	Decode(data, &h)
	if h.Counters[Updates] != want || h.Hists[RespTime].Count() != 61 || h.Gauges[RSS] != 90 {
		t.Fatalf("hour = %+v", h.Counters)
	}
	db.R.QueryRow(`SELECT data FROM m_day WHERE bot_id = 1`).Scan(&data)
	var d Minute
	Decode(data, &d)
	if d.Counters[Updates] != want {
		t.Fatalf("day = %d, want %d", d.Counters[Updates], want)
	}
	// Retention: minutes older than 30 days go.
	s.Rollup(ctx, hour.Add(31*24*time.Hour))
	var n int
	db.R.QueryRow(`SELECT COUNT(*) FROM m_minute`).Scan(&n)
	if n != 0 {
		t.Fatalf("old minutes kept: %d", n)
	}
}

func TestPullAcksAfterWrite(t *testing.T) {
	ctx := context.Background()
	db, _ := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	w := store.NewWriter(db, time.Hour, nil)
	defer func() { w.Close(); db.Close() }()
	s := &Storage{DB: db, W: w}
	ring := []Minute{sample(60, 1), sample(120, 2)}
	ring[0].ID, ring[1].ID = 1, 2
	var acked int64
	n, err := s.Pull(ctx,
		func(context.Context, int64) ([]Minute, error) {
			var out []Minute
			for _, m := range ring {
				if m.ID > acked {
					out = append(out, m)
				}
			}
			return out, nil
		},
		func(_ context.Context, up int64) error { acked = up; return nil })
	if err != nil || n != 2 || acked != 2 {
		t.Fatalf("n=%d acked=%d err=%v", n, acked, err)
	}
}

func TestQuery(t *testing.T) {
	ctx := context.Background()
	db, _ := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	w := store.NewWriter(db, time.Hour, nil)
	defer func() { w.Close(); db.Close() }()
	s := &Storage{DB: db, W: w}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var ms []Minute
	for i := range int64(30) {
		ms = append(ms, sample(now.Unix()-i*60, 10))
	}
	s.Save(ctx, ms)
	s.AddDeploy(ctx, 1, now.Add(-10*time.Minute), "v2", "ok")
	got, err := s.Query(ctx, 1, "1h", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Points) != 30 || got.Totals[Updates] != 300 || got.P50 <= 5 || got.P50 > 10 || len(got.Deploys) != 1 {
		t.Fatalf("%+v", got)
	}
	if _, err := s.Query(ctx, 1, "2h", now); err == nil {
		t.Fatal("bad period accepted")
	}
}
