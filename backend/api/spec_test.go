package spec_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	spec "github.com/SnimanOff/UTBPanel/backend/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/panel"
)

// Every operation must have an MCP level: a new API function must not
// appear in MCP by accident.
func TestEveryOperationHasMCPLevel(t *testing.T) {
	ops, err := spec.Operations()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, op := range ops {
		switch op.MCP {
		case spec.Read, spec.Write, spec.Confirm, spec.Never:
		default:
			t.Errorf("%s: x-mcp is %q, want read|write|confirm|never", op.Pattern(), op.MCP)
		}
		if op.ID == "" || ids[op.ID] {
			t.Errorf("%s: operationId %q missing or repeated", op.Pattern(), op.ID)
		}
		ids[op.ID] = true
	}
}

// Every route of the panel is described in openapi.yaml and vice versa.
func TestRoutesMatchSpec(t *testing.T) {
	dir := t.TempDir()
	run, _ := os.MkdirTemp("", "spec")
	defer os.RemoveAll(run)
	cfg := config.DefaultPanel()
	cfg.DataDir, cfg.RunDir = filepath.Join(dir, "panel"), run
	p, err := panel.New(context.Background(), cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer p.DB.Close()
	ops, _ := spec.Operations()
	var inSpec []string
	for _, op := range ops {
		inSpec = append(inSpec, op.Pattern())
	}
	for _, r := range p.API.Mux.Patterns {
		if !slices.Contains(inSpec, r) {
			t.Errorf("route %s is not in openapi.yaml", r)
		}
	}
	for _, s := range inSpec {
		if !slices.Contains(p.API.Mux.Patterns, s) {
			t.Errorf("openapi.yaml has %s, but no such route", s)
		}
	}
}
