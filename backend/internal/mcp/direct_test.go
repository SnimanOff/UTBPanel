package mcp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/bots"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

// The MCP token outside /mcp: only an upload, with the upload_release
// rules; never a deploy or anything else of the REST API.
func TestDirectUploadWithMCPToken(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.W.Exec(`INSERT INTO groups(id,name,mcp_mode) VALUES(1,'read','read'),(2,'full','full'),(3,'off','off')`)
	db.W.Exec(`INSERT INTO bot_meta(bot_id,group_id) VALUES(11,1),(22,2),(33,3)`)
	service, err := New(http.NotFoundHandler(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	service.Groups = &bots.Groups{DB: db}
	reached := 0
	h := service.Direct(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }))
	send := func(source api.Source, method, path string) int {
		r := httptest.NewRequest(method, path, nil)
		r = r.WithContext(api.WithCaller(r.Context(), api.Caller{Identity: pki.Identity{Serial: "s", Name: "laptop", Kind: pki.Owner}, Source: source}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	cases := []struct {
		source api.Source
		method string
		path   string
		pass   bool
	}{
		{api.SourceMCP, "POST", "/api/bots/22/releases", true},
		{api.SourceMCP, "POST", "/mcp", true},
		{api.SourceMCP, "GET", "/llms.txt", true},
		{api.SourceMCP, "GET", "/docs/ai.md", true},
		{api.SourceMCP, "POST", "/llms.txt", false},
		{api.SourceMCP, "POST", "/api/bots/11/releases", false}, // read-only group
		{api.SourceMCP, "POST", "/api/bots/33/releases", false}, // MCP off
		{api.SourceMCP, "POST", "/api/bots/44/releases", false}, // no group: read only
		{api.SourceMCP, "POST", "/api/bots/22/releases/v1/switch", false},
		{api.SourceMCP, "GET", "/api/bots/22/releases", false},
		{api.SourceMCP, "DELETE", "/api/bots/22", false},
		{api.SourceUI, "DELETE", "/api/bots/22", true},
	}
	for _, c := range cases {
		before := reached
		code := send(c.source, c.method, c.path)
		if passed := reached > before; passed != c.pass {
			t.Errorf("%s %s %s: passed=%v code=%d", c.source, c.method, c.path, passed, code)
		}
	}
}
