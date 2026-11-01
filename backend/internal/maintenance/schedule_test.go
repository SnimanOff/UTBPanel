package maintenance

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func newService(t *testing.T) *Service {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "core.db"), migrations.Core())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Service{DB: db}
}

func TestScheduleDeployAndOverdue(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	start := time.Now().Add(time.Hour)
	if err := s.Schedule(ctx, Plan{BotID: 1, StartsAt: start, EndsAt: start.Add(-time.Minute)}); err != ErrTimes {
		t.Fatalf("bad times: %v", err)
	}
	if err := s.Schedule(ctx, Plan{BotID: 1, StartsAt: start, EndsAt: start.Add(time.Hour), Version: "v2", AutoEnd: true, WarnHours: 2}); err != nil {
		t.Fatal(err)
	}
	var deployed []string
	var evs []string
	var warned string
	h := Hooks{
		Deploy: func(_ context.Context, _ int64, v string) error { deployed = append(deployed, v); return nil },
		Event:  func(_ int64, typ string, _ map[string]any) { evs = append(evs, typ) },
		Warn:   func(_ context.Context, _ M, text string) error { warned = text; return nil },
	}
	s.Tick(ctx, h, time.Now())
	if warned == "" {
		t.Fatal("no early warning within 2 hours of the start")
	}
	if s.IsActive(ctx, 1) {
		t.Fatal("started early")
	}
	s.Tick(ctx, h, start.Add(time.Second))
	if len(deployed) != 1 || s.IsActive(ctx, 1) {
		t.Fatalf("deploy %v; auto end must close maintenance", deployed)
	}

	// A failed deploy keeps maintenance; the end passes → overdue, extend.
	s.Schedule(ctx, Plan{BotID: 2, StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Minute), Version: "v3", AutoEnd: true})
	h.Deploy = func(context.Context, int64, string) error { return errors.New("crashed") }
	s.Tick(ctx, h, time.Now())
	if !s.IsActive(ctx, 2) || evs[len(evs)-1] != "maintenance_deploy_failed" {
		t.Fatalf("events %v", evs)
	}
	s.Tick(ctx, h, time.Now().Add(2*time.Minute))
	if evs[len(evs)-1] != "maintenance_overdue" {
		t.Fatalf("events %v", evs)
	}
	if err := s.Extend(ctx, 2, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Get(2)
	if m.Overdue || m.EndsAt.Before(time.Now().Add(29*time.Minute)) {
		t.Fatalf("extend: %+v", m)
	}
}
