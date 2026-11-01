package panics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func TestGroups(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s := &Store{DB: db, Now: func() time.Time { return now }}
	p := Panic{BotID: 1, Kind: "boom", Frame: "main.f (a.go:1)", Signature: "boom @ main.f", Stack: "s1", Version: "v1"}
	p.Time = now.Add(-48 * time.Hour)
	s.Add(ctx, p)
	p.Time, p.Version, p.Stack = now.Add(-30*time.Hour), "v2", "s2"
	s.Add(ctx, p)
	s.Add(ctx, Panic{BotID: 1, Time: now.Add(-time.Hour), Kind: "other", Signature: "other @ main.g", Version: "v2"})

	gs, _ := s.Groups(ctx, 1)
	if len(gs) != 2 {
		t.Fatalf("groups %d", len(gs))
	}
	g := gs[1]
	if g.Count != 2 || len(g.Versions) != 2 || g.LastStack != "s2" || g.Count24h != 0 || g.MaybeFixed {
		t.Fatalf("%+v", g)
	}
	if sum, _ := s.Summary(ctx); sum[1] != 1 {
		t.Fatalf("24 h summary %v", sum)
	}
	// v3 deployed 25 h ago and the "boom" did not come back.
	db.W.Exec(`INSERT INTO deploys (bot_id, ts, version, result) VALUES (1, ?, 'v3', 'ok')`, now.Add(-25*time.Hour).Unix())
	gs, _ = s.Groups(ctx, 1)
	if !gs[1].MaybeFixed || gs[0].MaybeFixed {
		t.Fatalf("maybe fixed: %v / %v", gs[1].MaybeFixed, gs[0].MaybeFixed)
	}
}
