package mcp

import (
	"bytes"
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
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type deviceTransport string

func (name deviceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	r.Header.Set("X-Test-Device", string(name))
	return http.DefaultTransport.RoundTrip(r)
}

func TestStreamableToolsValidateInputsBindDevicesAndAuditReads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	writer := store.NewWriter(db, time.Hour, log)
	defer writer.Close()
	auditLog := &audit.Log{DB: db, W: writer, Log: log}
	mux := api.NewMux()
	var reads, changes atomic.Int64
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) {
		c := api.CallerFrom(r.Context())
		api.WriteJSON(w, 200, map[string]string{"name": c.Name, "serial": c.Serial, "source": string(c.Source)})
	})
	mux.HandleFunc("GET /api/bots/{id}", func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		api.WriteJSON(w, 200, map[string]any{"id": json.Number(r.PathValue("id")), "large": json.Number("9007199254740993")})
	})
	mux.HandleFunc("GET /api/bots/{id}/db/tables/{table}", func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		api.WriteJSON(w, 200, map[string]string{"table": r.PathValue("table"), "limit": r.URL.Query().Get("limit")})
	})
	mux.HandleFunc("POST /api/bots/{id}/stop", func(w http.ResponseWriter, _ *http.Request) { changes.Add(1); w.WriteHeader(204) })
	service, err := New(auditLog.Middleware(mux), auditLog, log)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name := r.Header.Get("X-Test-Device"); name != "" {
			r = r.WithContext(api.WithCaller(r.Context(), api.Caller{Identity: pki.Identity{Name: name, Serial: name, Kind: pki.Owner}, IP: "127.0.0.1", Source: api.SourceMCP}))
		}
		service.ServeHTTP(w, r)
	}))
	defer server.Close()
	connect := func(name string) *sdk.ClientSession {
		t.Helper()
		client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
		session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: deviceTransport(name)}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { session.Close() })
		return session
	}
	a := connect("owner-A")
	b := connect("owner-B")
	tools, err := a.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]*sdk.Tool{}
	for _, tool := range tools.Tools {
		listed[tool.Name] = tool
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("#/components/")) {
			t.Fatal("unresolved OpenAPI reference", tool.Name)
		}
	}
	if listed["get_bot"] == nil || listed["get_bot_overview"] == nil || listed["set_flag"] == nil || listed["delete_bot"] != nil || listed["create_full_backup"] != nil || listed["set_secret"] != nil {
		t.Fatal("tool levels are not respected")
	}
	invoke := func(session *sdk.ClientSession, name string, args any, wantError bool) *sdk.CallToolResult {
		t.Helper()
		result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != wantError {
			t.Fatalf("%s: %+v; %v", name, result, result.Content[0].(*sdk.TextContent).Text)
		}
		return result
	}
	for _, item := range []struct {
		session *sdk.ClientSession
		name    string
	}{{a, "owner-A"}, {b, "owner-B"}, {a, "owner-A"}} {
		result := invoke(item.session, "ping", map[string]any{}, false)
		text := result.Content[0].(*sdk.TextContent).Text
		if !strings.Contains(text, item.name) || !strings.Contains(text, `"source":"mcp"`) {
			t.Fatal("device identity crossed requests", text)
		}
	}
	result := invoke(a, "get_bot", map[string]any{"id": json.Number("9007199254740993")}, false)
	if !strings.Contains(result.Content[0].(*sdk.TextContent).Text, "9007199254740993") {
		t.Fatal("large integer rounded")
	}
	invoke(a, "get_bot", map[string]any{}, true)
	invoke(a, "get_bot", map[string]any{"id": "7"}, true)
	invoke(a, "get_bot", map[string]any{"id": 7, "unexpected": "private-sentinel"}, true)
	invoke(a, "get_db_rows", map[string]any{"id": 7, "table": "../../keys"}, true)
	invoke(a, "stop", map[string]any{"id": 7}, true)
	result = invoke(a, "get_db_rows", map[string]any{"id": 7, "table": "orders", "limit": 3}, false)
	if !strings.Contains(result.Content[0].(*sdk.TextContent).Text, `"limit":"3"`) {
		t.Fatal("query parameter was not forwarded")
	}
	if reads.Load() != 2 || changes.Load() != 0 {
		t.Fatal("invalid or forbidden input reached the API", reads.Load(), changes.Load())
	}
	rows, err := auditLog.List(ctx, audit.Query{Source: "mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 10 {
		t.Fatalf("calls missing from audit: %d %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.Key != "owner-A" && row.Key != "owner-B" {
			t.Fatal(row)
		}
		if strings.Contains(row.Result, "private-sentinel") || bytes.Contains(row.Details, []byte("private-sentinel")) {
			t.Fatal("arguments leaked to audit")
		}
	}
	response, err := http.Post(server.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("missing device accepted")
	}
}
