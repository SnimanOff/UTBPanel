//go:build linux

package dbgate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// A bot puts links to a core database in place of its own files: restoring
// a backup and browsing must not write to or read from the linked file.
func TestBotLinksReachNoCoreFile(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	ctx := context.Background()
	base := t.TempDir()
	os.Chmod(base, 0o711)
	const uid = 12346
	core := filepath.Join(base, "core.db")
	db, err := sql.Open("sqlite", "file:"+core)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE secrets (v TEXT); INSERT INTO secrets VALUES ('master')`)
	db.Close()
	os.Chmod(core, 0o600)
	before, _ := os.ReadFile(core)

	// As in /data/core: the bot passes through, lists nothing.
	os.Chmod(filepath.Dir(base), 0o711)
	os.Mkdir(filepath.Join(base, "bots"), 0o711)
	botDir := filepath.Join(base, "bots", "1")
	os.MkdirAll(botDir, 0o770)
	os.Chown(botDir, 0, uid)
	os.Chmod(botDir, 0o1770)
	botDB := filepath.Join(botDir, "bot.db")
	backups := &Backups{B: &Browser{Path: func(int64) string { return botDB }, UID: func(int64) int { return uid }},
		Dir: func(int64) string { return filepath.Join(base, "backups") }}
	os.MkdirAll(filepath.Join(base, "backups"), 0o700)
	os.WriteFile(filepath.Join(base, "backups", "old.db"), []byte("backup"), 0o600)

	// Restore: bot.db.restore is a link to core.db.
	if err := os.Symlink(core, botDB+".restore"); err != nil {
		t.Fatal(err)
	}
	if err := backups.Restore(1, "old.db", nil); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(core); string(after) != string(before) {
		t.Fatal("restore wrote through the link into core.db")
	}

	// Browsing: bot.db is a link to core.db.
	os.Remove(botDB)
	if err := os.Symlink(core, botDB); err != nil {
		t.Fatal(err)
	}
	if r, err := backups.B.Query(ctx, 1, `SELECT v FROM secrets`, false); err == nil {
		t.Fatalf("the core database was read through the link: %+v", r.Rows)
	}
	if info, err := backups.B.Tables(ctx, 1); err == nil && len(info.Tables) > 0 {
		t.Fatalf("core tables listed: %+v", info.Tables)
	}

	// The backup before a deploy: a link must not copy core.db into it (an
	// autorollback would restore that copy and give it to the bot).
	if b, err := backups.Make(ctx, 1, "deploy"); err == nil {
		t.Fatalf("core.db backed up through the link: %+v", b)
	}
	os.Remove(botDB)
	if err := withIdentity(uid, func() error {
		own, err := sql.Open("sqlite", "file:"+botDB)
		if err != nil {
			return err
		}
		defer own.Close()
		_, err = own.Exec(`CREATE TABLE orders (id INTEGER); INSERT INTO orders VALUES (1)`)
		return err
	}); err != nil {
		t.Fatalf("the bot creates its database: %v", err)
	}
	b, err := backups.Make(ctx, 1, "deploy")
	if err != nil || !b.OK {
		t.Fatalf("own database not backed up: %+v %v", b, err)
	}
	fi, err := os.Lstat(filepath.Join(base, "backups", b.Name))
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v %v", fi, err)
	}
}
