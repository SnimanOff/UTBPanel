package bots

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

func TestCreateRejectsMalformedBodyBeforeTakingGroupGuard(t *testing.T) {
	groups := &Groups{}
	_, unlock, err := groups.Guard.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	s := &Service{Groups: groups}
	w := httptest.NewRecorder()
	s.create(w, httptest.NewRequest("POST", "/api/bots", strings.NewReader("{")).WithContext(ctx))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed request: HTTP %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateReportsGroupFailureAfterBotWasCreated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	dir, err := os.MkdirTemp("", "bot-create-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "c.sock")
	ln, err := wire.Listen(path, 0o600, -1)
	if err != nil {
		t.Fatal(err)
	}
	mux := wire.NewMux()
	mux.Handle("bots.create", wire.Func(func(_ context.Context, _ *wire.Conn, in core.CreateBot) (core.Bot, error) {
		return core.Bot{ID: 42, Name: in.Name, Username: "test_bot"}, nil
	}))
	srv := &wire.Server{Mux: mux, Hello: app.Hello(wire.ProtoCorePanel, "core", 1, 1)}
	serverDone := make(chan error, 1)
	go func() { serverDone <- srv.Serve(ln) }()
	defer func() {
		_ = ln.Close()
		if err := <-serverDone; err != nil {
			t.Errorf("wire server: %v", err)
		}
	}()
	client := &wire.Client{Path: path, Hello: app.Hello(wire.ProtoCorePanel, "panel", 1, 1)}
	go client.Run(ctx)
	conn, err := client.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	s := &Service{Core: client, Groups: &Groups{DB: db}}
	w := httptest.NewRecorder()
	s.create(w, httptest.NewRequest("POST", "/api/bots", strings.NewReader(`{"name":"Test","token":"123:fixture","group_id":999}`)).WithContext(ctx))
	var out struct {
		ID                    int64 `json:"id"`
		GroupAssignmentFailed bool  `json:"group_assignment_failed"`
	}
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.ID != 42 || !out.GroupAssignmentFailed {
		t.Fatalf("expected created bot with group warning, got HTTP %d: %s", w.Code, w.Body.String())
	}
}
