package core

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// MkdirAll and WriteFile build the bot directory the way the runner does.
func seedBotDir(t *testing.T, dir string) {
	t.Helper()
	for _, d := range []string{"files", "files/sub", "releases/v1", "secrets", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(d)), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"bot.db":            "state",
		"bot.db-wal":        "write-ahead log",
		"bot.db-shm":        "shared memory",
		"bot.db.restore":    "leftover of a restore",
		"files/data.txt":    "user data",
		"files/sub/more.db": "more",
		"releases/v1/bot":   "binary",
		"secrets/API_KEY":   "secret",
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func insertBot(t *testing.T, c *Core, id int64) {
	t.Helper()
	if _, err := c.DB.W.Exec(`INSERT INTO bots (id, name, port, created_at) VALUES (?, 'test', 20000, 'x')`, id); err != nil {
		t.Fatal(err)
	}
}

func TestClearBotWipesDataAndKeepsTheBot(t *testing.T) {
	ctx := context.Background()
	c := newCore(t)
	insertBot(t, c, 7)
	dir := c.Runner.BotDir(7)
	seedBotDir(t, dir)

	if err := c.ClearBot(ctx, 7); err != nil {
		t.Fatalf("clear: %v", err)
	}

	for _, name := range []string{"bot.db", "bot.db-wal", "bot.db-shm", "bot.db.restore", "files/data.txt", "files/sub/more.db"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survived the clear", name)
		}
	}
	// DATA_DIR itself, the versions and the secrets are the bot's contract.
	for _, name := range []string{"files", "releases/v1/bot", "secrets/API_KEY"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s must stay: %v", name, err)
		}
	}
	var n int
	if err := c.DB.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM bots WHERE id = 7`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("bot rows: %d, want 1", n)
	}
}

func TestClearBotRefusesAnUnknownBot(t *testing.T) {
	if err := newCore(t).ClearBot(context.Background(), 99); !errors.Is(err, errNoBot) {
		t.Fatalf("got %v, want errNoBot", err)
	}
}

// A bot owns files/ and can put a link there; the clear must remove the link,
// never write through it to core.db or the master key.
func TestClearBotRemovesLinksNotTheirTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need a privilege on Windows")
	}
	ctx := context.Background()
	c := newCore(t)
	insertBot(t, c, 7)
	dir := c.Runner.BotDir(7)
	seedBotDir(t, dir)
	target := filepath.Join(c.Cfg.DataDir, "core.db")
	if err := os.Symlink(target, filepath.Join(dir, "files", "innocent.txt")); err != nil {
		t.Fatal(err)
	}

	if err := c.ClearBot(ctx, 7); err != nil {
		t.Fatalf("clear: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "files", "innocent.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the link must be removed, got %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("core.db followed the link: %v", err)
	}
}
