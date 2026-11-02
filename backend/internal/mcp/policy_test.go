package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/bots"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func TestMCPGroupPolicyChecksCurrentScopeAndFiltersGlobalLists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{
		`INSERT INTO groups(id,name,mcp_mode) VALUES(1,'read','read'),(2,'full','full'),(3,'off','off')`,
		`INSERT INTO bot_meta(bot_id,group_id) VALUES(11,1),(22,2),(33,3)`,
		`INSERT INTO alert_rules(id,bot_id,metric,value) VALUES(101,11,'cpu',80),(102,22,'cpu',80),(103,0,'cpu',80)`,
	} {
		if _, err := db.W.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	groups := &bots.Groups{DB: db}
	mux := api.NewMux()
	var writes atomic.Int64
	mux.HandleFunc("GET /api/bots/{id}", func(w http.ResponseWriter, r *http.Request) {
		api.WriteJSON(w, 200, map[string]string{"id": r.PathValue("id")})
	})
	mux.HandleFunc("PATCH /api/bots/{id}", func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		api.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/bots/{id}/stop", func(w http.ResponseWriter, r *http.Request) { writes.Add(1); w.WriteHeader(204) })
	mux.HandleFunc("POST /api/alert-rules", func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		api.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/bots", func(w http.ResponseWriter, r *http.Request) {
		api.WriteJSON(w, 200, []map[string]int{{"id": 11}, {"id": 22}, {"id": 33}, {"id": 44}})
	})
	mux.HandleFunc("GET /api/audit", func(w http.ResponseWriter, r *http.Request) {
		api.WriteJSON(w, 200, []map[string]any{{"bot_id": 11, "text": "visible"}, {"bot_id": 33, "text": "hidden"}, {"text": "global"}})
	})
	mux.HandleFunc("GET /api/panics/summary", func(w http.ResponseWriter, r *http.Request) { api.WriteJSON(w, 200, map[string]int{"11": 1, "33": 2}) })
	service, err := New(mux, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	service.Groups = groups
	ops, err := operations()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]operation{}
	for _, op := range ops {
		byName[op.ID] = op
	}
	ctx = api.WithCaller(ctx, api.Caller{Identity: pki.Identity{Serial: "owner", Name: "owner", Kind: pki.Owner}, Source: api.SourceMCP})
	invoke := func(name string, args any, wantError bool) string {
		t.Helper()
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		result := service.call(ctx, byName[name], raw)
		text, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != wantError {
			t.Fatalf("%s: %s", name, text)
		}
		return string(text)
	}
	for _, id := range []int{11, 22, 44} {
		invoke("get_bot", map[string]any{"id": id}, false)
	}
	invoke("get_bot", map[string]any{"id": 33}, true)
	rename := func(id int, wantError bool) {
		invoke("rename_bot", map[string]any{"id": id, "body": map[string]string{"name": "changed"}}, wantError)
	}
	rename(11, true)
	rename(44, true)
	rename(22, false)
	invoke("stop", map[string]any{"id": 22}, true)
	if writes.Load() != 1 {
		t.Fatal("read-only or confirmation operation executed", writes.Load())
	}
	if _, err := groups.SetMCPMode(ctx, 2, "read"); err != nil {
		t.Fatal(err)
	}
	rename(22, true)
	if _, err := groups.SetMCPMode(ctx, 2, "full"); err != nil {
		t.Fatal(err)
	}
	if err := groups.SetGroup(ctx, 22, 3); err != nil {
		t.Fatal(err)
	}
	rename(22, true)
	invoke("get_bot", map[string]any{"id": 22}, true)
	if err := groups.SetGroup(ctx, 22, 2); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"list_bots", "get_audit", "panics_summary"} {
		text := invoke(name, map[string]any{}, false)
		if strings.Contains(text, "33") || strings.Contains(text, "hidden") {
			t.Fatal("disabled bot leaked through", name, text)
		}
	}
	rule := func(id, bot int, wantError bool) {
		invoke("save_alert_rule", map[string]any{"body": map[string]any{"id": id, "bot_id": bot, "metric": "cpu", "value": 80, "minutes": 5}}, wantError)
	}
	rule(101, 22, true)
	rule(103, 22, true)
	rule(0, 0, true)
	rule(102, 11, false)
	rule(0, 22, false)
	invoke("read_notifications", map[string]any{"body": map[string]any{"ids": []int{1}}}, true)
	create := byName["create_bot"]
	args := map[string]any{"body": map[string]any{"name": "new", "token": "private"}}
	if err := service.authorize(ctx, create, args); err != nil {
		t.Fatal(err)
	}
	if inputID(args["body"].(map[string]any)["group_id"]) != 2 {
		t.Fatal("default creation group is not full", args)
	}
	args["body"].(map[string]any)["group_id"] = json.Number("1")
	if err := service.authorize(ctx, create, args); err == nil {
		t.Fatal("creation in read-only group permitted")
	}
	if _, err := groups.SetMCPMode(ctx, 2, "read"); err != nil {
		t.Fatal(err)
	}
	args["body"] = map[string]any{"name": "new", "token": "private"}
	if err := service.authorize(ctx, create, args); err == nil {
		t.Fatal("creation permitted without any full group")
	}
}

func TestMCPModesAreOwnerUIOnly(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	groups := &bots.Groups{DB: db}
	group, err := groups.Create(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if group.MCPMode != "read" {
		t.Fatal("new groups do not default to read")
	}
	service := &Service{Groups: groups}
	mux := api.NewMux()
	service.Register(mux)
	for _, item := range []struct {
		kind   pki.Kind
		source api.Source
		body   string
		status int
	}{
		{pki.Child, api.SourceUI, `{"mode":"full"}`, 403}, {pki.Owner, api.SourceMCP, `{"mode":"full"}`, 403}, {pki.Owner, api.SourceUI, `{"mode":"invalid"}`, 400}, {pki.Owner, api.SourceUI, `{"mode":"full","other":1}`, 400}, {pki.Owner, api.SourceUI, `{"mode":"full"}`, 204},
	} {
		r := httptest.NewRequest("PUT", "http://127.0.0.1:47100/api/mcp/groups/1", strings.NewReader(item.body))
		r = r.WithContext(api.WithCaller(ctx, api.Caller{Identity: pki.Identity{Kind: item.kind}, Source: item.source}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != item.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	list, err := groups.List(ctx)
	if err != nil || len(list) != 1 || list[0].MCPMode != "full" {
		t.Fatal(list, err)
	}
}
