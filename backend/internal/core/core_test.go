package core

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
)

func newCore(t *testing.T) *Core {
	t.Helper()
	dir := t.TempDir()
	run, _ := os.MkdirTemp("", "core")
	t.Cleanup(func() { os.RemoveAll(run) })
	cfg := config.DefaultCore()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.KeysDir = filepath.Join(dir, "keys")
	cfg.RunDir = run
	os.MkdirAll(cfg.KeysDir, 0o700)
	c, err := New(context.Background(), cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Logs.Close(); c.W.Close(); c.DB.Close() })
	return c
}

func TestNoKeysVolume(t *testing.T) {
	cfg := config.DefaultCore()
	cfg.DataDir = t.TempDir()
	cfg.KeysDir = filepath.Join(t.TempDir(), "missing")
	if _, err := New(context.Background(), cfg, slog.Default()); !errors.Is(err, secrets.ErrNoKeysDir) {
		t.Fatalf("got %v", err)
	}
}

func TestImportRefusedWithBots(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	key := make([]byte, secrets.KeySize)
	if err := c.importMasterKey(ctx, key); err != nil {
		t.Fatalf("import on empty core: %v", err)
	}
	c.DB.W.Exec(`INSERT INTO bots (id, name, port, created_at) VALUES (1, 'a', 20001, 'x')`)
	if err := c.importMasterKey(ctx, key); !errors.Is(err, secrets.ErrHasBots) {
		t.Fatalf("import with bots: %v", err)
	}
}
