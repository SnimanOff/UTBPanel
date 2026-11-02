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

	spec "github.com/SnimanOff/UTBPanel/backend/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/bots"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func levelsDB(t *testing.T, ctx context.Context) *store.DB {
	t.Helper()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`INSERT INTO groups(id,name,mcp_mode) VALUES(1,'read','read'),(2,'full','full'),(3,'off','off')`,
		`INSERT INTO bot_meta(bot_id,group_id) VALUES(11,1),(22,2),(33,3)`,
	} {
		if _, err := db.W.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// Every tool, in every group mode: off refuses everything, read lets only
// read-level tools through, full allows all of them (confirm ones are
// checked here and confirmed separately). A bot without a group is read.
func TestLevelsInEveryMode(t *testing.T) {
	ctx := context.Background()
	service := &Service{Groups: &bots.Groups{DB: levelsDB(t, ctx)}}
	ops, err := operations()
	if err != nil {
		t.Fatal(err)
	}
	modes := map[int64]string{11: "read", 22: "full", 33: "off", 44: "read"}
	var scoped, global int
	for _, op := range ops {
		switch {
		case op.ID == "create_bot":
			continue
		case strings.HasPrefix(op.Path, "/api/bots/{id}"):
			scoped++
			for bot, mode := range modes {
				err := service.authorize(ctx, op, map[string]any{"id": json.Number(jsonInt(bot))})
				want := mode == "full" || (mode == "read" && op.MCP == spec.Read)
				if (err == nil) != want {
					t.Errorf("%s (%s) in %s mode: %v", op.ID, op.MCP, mode, err)
				}
			}
			if service.authorize(ctx, op, map[string]any{"id": json.Number("0")}) == nil {
				t.Errorf("%s: no bot", op.ID)
			}
		case op.ID == "save_alert_rule" || op.ID == "delete_alert_rule":
			// Covered with existing rules in the group policy test.
		default:
			global++
			err := service.authorize(ctx, op, map[string]any{})
			if (err == nil) != (op.MCP == spec.Read) {
				t.Errorf("%s (%s) without a bot: %v", op.ID, op.MCP, err)
			}
			// A bot filter cannot reach a disabled group.
			if service.authorize(ctx, op, map[string]any{"bot": json.Number("33")}) == nil {
				t.Errorf("%s: bot in a disabled group", op.ID)
			}
		}
	}
	if scoped < 20 || global < 5 {
		t.Fatal("too few operations checked", scoped, global)
	}
	// Without the group table the server stays read-only.
	bare := &Service{}
	for _, op := range ops {
		if err := bare.authorize(ctx, op, map[string]any{"id": json.Number("22")}); (err == nil) != (op.MCP == spec.Read) {
			t.Errorf("%s without groups: %v", op.ID, err)
		}
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// What would let an AI widen its own rights is not a tool at all, and
// cannot be reached by name, by an extra body field or through the API.
func TestPrivilegeEscalation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db := levelsDB(t, ctx)
	all, err := spec.Operations()
	if err != nil {
		t.Fatal(err)
	}
	levels := map[string]string{}
	for _, op := range all {
		levels[op.ID] = op.MCP
	}
	never := []string{
		"set_bot_group", "set_mcp_mode", "get_mcp_modes", "confirm_mcp_action", "reject_mcp_action",
		"issue_key", "revoke_key", "list_keys", "approve_device",
		"delete_bot", "set_token", "set_secret", "create_broadcast",
		"exec_db", "restore_db", "ban_user_everywhere",
		"export_bots", "create_full_backup", "set_full_backup_settings", "apply_bot_import",
		"create_group", "delete_group", "set_panel_settings",
	}
	mux := api.NewMux()
	var reached atomic.Int64
	for _, op := range all {
		mux.HandleFunc(op.Pattern(), func(w http.ResponseWriter, r *http.Request) {
			reached.Add(1)
			api.WriteJSON(w, 200, map[string]bool{"ok": true})
		})
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := New(mux, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	service.Groups = &bots.Groups{DB: db}
	service.DB = db
	service.Notes = &alerts.Service{DB: db}
	tools, err := Tools()
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, tool := range tools {
		listed[tool.Name] = true
	}
	for _, name := range never {
		if levels[name] != spec.Never {
			t.Errorf("%s: level %q, want never", name, levels[name])
		}
		if _, ok := service.ops[name]; ok || listed[name] {
			t.Errorf("%s is a tool", name)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(api.WithCaller(r.Context(), api.Caller{Identity: pki.Identity{Name: "laptop", Serial: "laptop", Kind: pki.Owner}, IP: "127.0.0.1", Source: api.SourceMCP}))
		service.ServeHTTP(w, r)
	}))
	defer server.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: server.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, item := range []struct {
		name string
		args map[string]any
	}{
		{"set_mcp_mode", map[string]any{"gid": 1, "body": map[string]any{"mode": "full"}}},
		{"set_bot_group", map[string]any{"id": 11, "body": map[string]any{"group_id": 2}}},
		{"issue_key", map[string]any{"body": map[string]any{"name": "ai"}}},
		{"confirm_mcp_action", map[string]any{"aid": "x"}},
		{"delete_bot", map[string]any{"id": 22}},
		// Allowed tools do not take fields they do not declare.
		{"apply_config", map[string]any{"id": 22, "body": map[string]any{"version": "1", "yaml": "", "group_id": 2}}},
		{"rename_bot", map[string]any{"id": 11, "group_id": 2, "body": map[string]any{"name": "x"}}},
		// Nor ids smuggled into a path.
		{"get_bot", map[string]any{"id": "33/../11"}},
	} {
		res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: item.name, Arguments: item.args})
		if err == nil && !res.IsError {
			t.Errorf("%s: accepted", item.name)
		}
	}
	if n := reached.Load(); n != 0 {
		t.Fatal("handlers reached", n)
	}

	// The panel API refuses the MCP source for owner-only changes, even
	// with the owner's key behind it.
	groups := &Service{Groups: &bots.Groups{DB: db}}
	gmux := api.NewMux()
	groups.Register(gmux)
	mcpCaller := api.Caller{Identity: pki.Identity{Serial: "laptop", Kind: pki.Owner}, Source: api.SourceMCP}
	for _, req := range []struct{ method, path, body string }{
		{"PUT", "/api/mcp/groups/1", `{"mode":"full"}`},
		{"POST", "/api/mcp/actions/x/confirm", ``},
	} {
		r := httptest.NewRequest(req.method, "http://127.0.0.1:47100"+req.path, strings.NewReader(req.body))
		r = r.WithContext(api.WithCaller(ctx, mcpCaller))
		w := httptest.NewRecorder()
		gmux.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d %s", req.method, req.path, w.Code, w.Body.String())
		}
	}
	var mode string
	db.R.QueryRow(`SELECT mcp_mode FROM groups WHERE id = 1`).Scan(&mode)
	if mode != "read" {
		t.Fatal("mode changed", mode)
	}
}

// The owner confirms the diff for one version of the settings; if they
// change meanwhile, the confirmed request fails instead of applying over
// them, and the AI sees the conflict.
func TestConfirmedConfigConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db := levelsDB(t, ctx)
	var current atomic.Value
	current.Store("rev1")
	var applied atomic.Int64
	mux := api.NewMux()
	mux.HandleFunc("POST /api/bots/{id}/config/diff", func(w http.ResponseWriter, r *http.Request) {
		api.WriteJSON(w, 200, map[string]any{"changes": []any{map[string]any{"path": "env.A", "level": "confirm"}}})
	})
	mux.HandleFunc("POST /api/bots/{id}/config/apply", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Version string }
		json.NewDecoder(r.Body).Decode(&body)
		if body.Version != current.Load().(string) {
			api.WriteError(w, http.StatusConflict, "conflict", "settings changed")
			return
		}
		applied.Add(1)
		current.Store("rev-applied")
		api.WriteJSON(w, 200, map[string]any{"version": "rev-applied"})
	})
	service, err := New(mux, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	service.Groups = &bots.Groups{DB: db}
	service.DB = db
	service.Notes = &alerts.Service{DB: db}
	mctx := api.WithCaller(ctx, api.Caller{Identity: pki.Identity{Serial: "laptop", Name: "laptop", Kind: pki.Owner}, Source: api.SourceMCP})
	request := func() string {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"id": 22, "body": map[string]any{"version": "rev1", "yaml": "env: {A: '2'}"}})
		r := service.call(mctx, service.ops["apply_config"], raw)
		m, _ := r.StructuredContent.(map[string]any)
		if r.IsError || m["status"] != "pending" {
			t.Fatalf("%+v", r.Content)
		}
		if applied.Load() != 0 {
			t.Fatal("applied before confirmation")
		}
		return m["id"].(string)
	}

	id := request()
	current.Store("rev2") // the owner edited the settings in the panel
	a, err := service.Decide(ctx, id, true, "owner")
	if err != nil || a.State != "failed" || !strings.Contains(string(a.Result), "409") && !strings.Contains(string(a.Result), "conflict") {
		t.Fatalf("%+v %s %v", a, a.Result, err)
	}
	raw, _ := json.Marshal(map[string]any{"id": id})
	if r := service.wait(mctx, raw); r.StructuredContent.(map[string]any)["status"] != "failed" {
		t.Fatalf("%+v", r.StructuredContent)
	}
	if applied.Load() != 0 {
		t.Fatal("applied over newer settings")
	}

	current.Store("rev1")
	id = request()
	if a, err := service.Decide(ctx, id, true, "owner"); err != nil || a.State != "done" || applied.Load() != 1 {
		t.Fatalf("%+v %v", a, err)
	}
}
