package aidocs

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/mcp"
)

func TestDocsFollowTheSpec(t *testing.T) {
	llms, md, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := mcp.Tools()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
		if !strings.Contains(llms, "- "+tool.Name+" ("+tool.Level+")") {
			t.Errorf("llms.txt misses %s", tool.Name)
		}
		if !strings.Contains(md, "| `"+tool.Name+"`") {
			t.Errorf("ai.md misses %s", tool.Name)
		}
	}
	for _, never := range []string{"delete_bot", "set_mcp_mode", "exec_db", "set_secret"} {
		if names[never] || strings.Contains(llms, "- "+never+" (") {
			t.Errorf("%s is never a tool", never)
		}
	}
	// Tool names in the hand-written concepts must exist: a renamed
	// operation must not leave the guide pointing to nothing.
	for _, m := range regexp.MustCompile("`([a-z]+(?:_[a-z]+)+)`").FindAllStringSubmatch(md[:strings.Index(md, "## Tools")], -1) {
		if !names[m[1]] && m[1] != "x_mcp" {
			t.Errorf("the guide mentions %s, which is not a tool", m[1])
		}
	}
	for _, want := range []string{"## Concepts", "untrusted", "wait_action", "`env`", "\"$defs\"", "BotConfigDocument", "⚠"} {
		if !strings.Contains(md, want) {
			t.Errorf("ai.md misses %q", want)
		}
	}
	if !strings.HasPrefix(llms, "# TG Panel\n\n> ") || !strings.Contains(llms, "(/docs/ai.md)") {
		t.Fatalf("llms.txt header:\n%s", llms[:200])
	}
}

func TestServed(t *testing.T) {
	mux := api.NewMux()
	Register(mux)
	for path, kind := range map[string]string{"/llms.txt": "text/plain", "/docs/ai.md": "text/markdown"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), kind) || w.Body.Len() < 1000 {
			t.Errorf("%s: %d %s %d bytes", path, w.Code, w.Header().Get("Content-Type"), w.Body.Len())
		}
	}
}
