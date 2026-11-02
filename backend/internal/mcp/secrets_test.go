package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/bots"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/secretreq"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSecretsThroughMCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.W.Exec(`INSERT INTO groups(id,name,mcp_mode) VALUES(1,'read','read'),(2,'full','full')`); err != nil {
		t.Fatal(err)
	}
	db.W.Exec(`INSERT INTO bot_meta(bot_id,group_id) VALUES(22,2)`)
	asks := &secretreq.Store{DB: db, Notes: &alerts.Service{DB: db}}
	mux := api.NewMux()
	created := false
	mux.HandleFunc("POST /api/bots", func(w http.ResponseWriter, r *http.Request) { created = true; w.WriteHeader(201) })
	(&secretreq.API{Store: asks, WriteError: func(w http.ResponseWriter, err error) { api.WriteError(w, 500, "x", err.Error()) }}).Register(mux)
	service, err := New(mux, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	service.Groups = &bots.Groups{DB: db}
	service.DB = db
	service.Secrets = asks
	mctx := api.WithCaller(ctx, api.Caller{Identity: pki.Identity{Serial: "laptop", Name: "laptop", Kind: pki.Owner}, Source: api.SourceMCP})
	call := func(op string, args any) *sdk.CallToolResult {
		raw, _ := json.Marshal(args)
		return service.call(mctx, service.ops[op], raw)
	}
	status := func(r *sdk.CallToolResult) map[string]any {
		m, _ := r.StructuredContent.(map[string]any)
		return m
	}
	wait := func(id string, timeout int) map[string]any {
		raw, _ := json.Marshal(map[string]any{"id": id, "timeout": timeout})
		return status(service.wait(mctx, raw))
	}

	// A token never goes through MCP.
	if r := call("create_bot", map[string]any{"body": map[string]any{"name": "Shop", "token": "111:x"}}); !r.IsError || created {
		t.Fatal("token accepted through MCP")
	}
	r := call("create_bot", map[string]any{"body": map[string]any{"name": "Shop", "hint": "у @BotFather"}})
	m := status(r)
	if r.IsError || m["status"] != "waiting" || created {
		t.Fatalf("%+v %v", r.Content, m)
	}
	id := m["id"].(string)
	req, _ := asks.Get(ctx, id)
	if req.BotName != "Shop" || req.GroupID != 2 || req.Hint != "у @BotFather" {
		t.Fatalf("%+v", req)
	}
	if got := wait(id, 0); got["status"] != "waiting" {
		t.Fatalf("%v", got)
	}
	done := make(chan map[string]any)
	go func() { done <- wait(id, 10) }()
	time.Sleep(100 * time.Millisecond)
	asks.Created(ctx, id, 99, "owner")
	if got := <-done; got["status"] != "set" || got["bot_id"] != int64(99) {
		t.Fatalf("%v", got)
	}

	// request_secret needs a group in full mode and returns the state only.
	r = call("request_secret", map[string]any{"id": 22, "body": map[string]any{"name": "PAYMENT_KEY", "hint": "ЮKassa"}})
	if r.IsError {
		t.Fatalf("%+v", r.Content)
	}
	text, _ := json.Marshal(r.StructuredContent)
	if !strings.Contains(string(text), `"state":"waiting"`) {
		t.Fatalf("%s", text)
	}
	list, _ := asks.List(ctx, 22, true)
	if len(list) != 1 {
		t.Fatalf("%+v", list)
	}
	asks.Fulfilled(ctx, 22, "PAYMENT_KEY", "owner")
	if got := wait(list[0].ID, 0); got["status"] != "set" {
		t.Fatalf("%v", got)
	}
}
