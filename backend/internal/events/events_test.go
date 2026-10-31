package events

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

func TestEventReachesPanelThatWasDown(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	coreDB, err := store.Open(ctx, filepath.Join(dir, "core.db"), migrations.Core())
	if err != nil {
		t.Fatal(err)
	}
	defer coreDB.Close()
	panelDB, err := store.Open(ctx, filepath.Join(dir, "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer panelDB.Close()

	sockDir, _ := os.MkdirTemp("", "ev")
	defer os.RemoveAll(sockDir)
	sock := filepath.Join(sockDir, "core.sock")
	mux := wire.NewMux()
	srv := &wire.Server{Mux: mux, Hello: wire.Hello{Proto: wire.ProtoCorePanel, Version: 1}}
	bus := NewBus(coreDB, srv, mux, nil)
	ln, err := wire.Listen(sock, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go srv.Serve(ln)

	// The panel is down: events happen.
	bus.Emit(Test, 1, Info, map[string]any{"n": 1})
	bus.Emit(BotCrashed, 2, Error, map[string]any{"code": 2})

	pmux := wire.NewMux()
	sink := NewSink(panelDB, pmux, nil)
	got := make(chan Event, 10)
	sink.Subscribe(func(ev Event) { got <- ev })
	c, err := wire.Dial(ctx, sock, pmux, wire.Hello{Proto: wire.ProtoCorePanel, Version: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := sink.CatchUp(ctx, c); err != nil {
		t.Fatal(err)
	}
	if id, _ := sink.LastID(ctx); id != 2 {
		t.Fatalf("last id after catch-up = %d", id)
	}

	// Live push while connected.
	for range 2 {
		<-got
	}
	bus.Emit(Test, 0, Info, nil)
	select {
	case ev := <-got:
		if ev.ID != 3 || ev.Type != Test {
			t.Fatalf("pushed event = %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pushed event did not arrive")
	}
	// A repeated catch-up does not duplicate events.
	if err := sink.CatchUp(ctx, c); err != nil {
		t.Fatal(err)
	}
	evs, err := sink.Recent(ctx, 0, 10)
	if err != nil || len(evs) != 3 {
		t.Fatalf("recent = %d, %v", len(evs), err)
	}
	if evs[1].BotID != 2 || evs[1].Data["code"] != float64(2) {
		t.Fatalf("event data = %+v", evs[1])
	}
}

func TestSinceFromDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "core.db"), migrations.Core())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bus := NewBus(db, nil, wire.NewMux(), nil)
	for range 5 {
		bus.Emit(Test, 0, Info, nil)
	}
	// A restarted core has an empty ring and reads from core.db.
	bus2 := NewBus(db, nil, wire.NewMux(), nil)
	evs, err := bus2.Since(ctx, 2, 100)
	if err != nil || len(evs) != 3 || evs[0].ID != 3 {
		t.Fatalf("since = %+v, %v", evs, err)
	}
}
