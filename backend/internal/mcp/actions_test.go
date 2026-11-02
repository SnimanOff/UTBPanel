package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/bots"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPendingActions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`INSERT INTO groups(id,name,mcp_mode) VALUES(1,'read','read'),(2,'full','full')`,
		`INSERT INTO bot_meta(bot_id,group_id) VALUES(11,1),(22,2)`,
	} {
		if _, err := db.W.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	mux := api.NewMux()
	var deployed atomic.Value
	deployed.Store("")
	mux.HandleFunc("GET /api/bots/{id}/releases/{version}/config", func(w http.ResponseWriter, r *http.Request) {
		api.WriteJSON(w, 200, map[string]any{"yaml": "env: {A: '1'}", "settings_version": "rev1", "diff": map[string]any{"changes": []any{}}})
	})
	mux.HandleFunc("POST /api/bots/{id}/releases/{version}/switch", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		c := api.CallerFrom(r.Context())
		deployed.Store(r.PathValue("version") + "|" + body["config_version"] + "|" + string(c.Source) + ":" + c.Name)
		api.WriteJSON(w, 200, map[string]string{"version": r.PathValue("version")})
	})
	mux.HandleFunc("POST /api/bots/{id}/config/diff", func(w http.ResponseWriter, r *http.Request) {
		api.WriteJSON(w, 409, map[string]string{"error": "conflict"})
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := New(mux, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	service.Groups = &bots.Groups{DB: db}
	service.DB = db
	service.Notes = &alerts.Service{DB: db}
	device := api.Caller{Identity: pki.Identity{Serial: "laptop", Name: "laptop", Kind: pki.Owner}, Source: api.SourceMCP}
	mctx := api.WithCaller(ctx, device)
	call := func(op string, args any) *sdk.CallToolResult {
		raw, _ := json.Marshal(args)
		return service.call(mctx, service.ops[op], raw)
	}
	pendingID := func(r *sdk.CallToolResult) string {
		t.Helper()
		m, _ := r.StructuredContent.(map[string]any)
		if r.IsError || m["status"] != "pending" {
			t.Fatalf("want pending: %+v %v", r.Content, m)
		}
		return m["id"].(string)
	}
	state := func(r *sdk.CallToolResult) string {
		m, _ := r.StructuredContent.(map[string]any)
		s, _ := m["status"].(string)
		return s
	}
	wait := func(c context.Context, id string, timeout int) *sdk.CallToolResult {
		raw, _ := json.Marshal(map[string]any{"id": id, "timeout": timeout})
		return service.wait(c, raw)
	}

	// A read-only group does not get as far as a request.
	if r := call("deploy_release", map[string]any{"id": 11, "version": "v2"}); !r.IsError {
		t.Fatal("read-only group must refuse")
	}
	// A failing diff is reported at once, nothing waits.
	if r := call("apply_config", map[string]any{"id": 22, "body": map[string]any{"version": "x", "yaml": "env: {}"}}); !r.IsError {
		t.Fatal("conflicting diff must be an error")
	}

	id := pendingID(call("deploy_release", map[string]any{"id": 22, "version": "v2"}))
	if deployed.Load() != "" {
		t.Fatal("deployed before confirmation")
	}
	list, _ := service.Actions(ctx)
	if len(list) != 1 || list[0].Summary != "развернуть версию v2" || !strings.Contains(string(list[0].Detail), "rev1") || list[0].Requested != "laptop" {
		t.Fatalf("%+v", list)
	}
	var notes int
	db.R.QueryRow(`SELECT COUNT(*) FROM notifications WHERE key = ? AND resolved = 0`, "mcp:"+id).Scan(&notes)
	if notes != 1 {
		t.Fatal("no notification")
	}
	if r := wait(mctx, id, 0); state(r) != "pending" {
		t.Fatalf("%+v", r.StructuredContent)
	}
	// Another device does not see it.
	other := api.WithCaller(ctx, api.Caller{Identity: pki.Identity{Serial: "phone", Name: "phone"}, Source: api.SourceMCP})
	if r := wait(other, id, 0); !r.IsError {
		t.Fatal("another device sees the request")
	}

	// The owner confirms while the AI waits.
	done := make(chan *sdk.CallToolResult)
	go func() { done <- wait(mctx, id, 10) }()
	time.Sleep(100 * time.Millisecond)
	a, err := service.Decide(ctx, id, true, "owner")
	if err != nil || a.State != "done" {
		t.Fatalf("%+v %v", a, err)
	}
	if r := <-done; state(r) != "done" {
		t.Fatalf("%+v", r.StructuredContent)
	}
	// Run as the device, with the diff the owner saw.
	if got := deployed.Load(); got != "v2|rev1|mcp:laptop" {
		t.Fatalf("deployed %q", got)
	}
	if _, err := service.Decide(ctx, id, false, "owner"); err != errDecided {
		t.Fatalf("second decision: %v", err)
	}
	db.R.QueryRow(`SELECT COUNT(*) FROM notifications WHERE key = ? AND resolved = 0`, "mcp:"+id).Scan(&notes)
	if notes != 0 {
		t.Fatal("notification left open")
	}

	// Rejected, and a mode change between request and confirmation.
	id = pendingID(call("deploy_release", map[string]any{"id": 22, "version": "v3"}))
	if a, _ := service.Decide(ctx, id, false, "owner"); a.State != "rejected" {
		t.Fatalf("%+v", a)
	}
	if r := wait(mctx, id, 5); state(r) != "rejected" {
		t.Fatalf("%+v", r.StructuredContent)
	}
	id = pendingID(call("deploy_release", map[string]any{"id": 22, "version": "v4"}))
	db.W.Exec(`UPDATE groups SET mcp_mode = 'read' WHERE id = 2`)
	if a, _ := service.Decide(ctx, id, true, "owner"); a.State != "failed" || !strings.Contains(string(a.Result), "forbidden") {
		t.Fatalf("%+v %s", a, a.Result)
	}
	db.W.Exec(`UPDATE groups SET mcp_mode = 'full' WHERE id = 2`)

	// Expiry.
	id = pendingID(call("deploy_release", map[string]any{"id": 22, "version": "v5"}))
	db.W.Exec(`UPDATE mcp_actions SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Second).Unix(), id)
	if r := wait(mctx, id, 0); state(r) != "expired" {
		t.Fatalf("%+v", r.StructuredContent)
	}
	if _, err := service.Decide(ctx, id, true, "owner"); err != errDecided {
		t.Fatalf("expired confirm: %v", err)
	}
	if got := deployed.Load(); got != "v2|rev1|mcp:laptop" {
		t.Fatalf("deployed %q", got)
	}
}
