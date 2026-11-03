//go:build linux

package dbgate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A bot puts links to a core database in place of its own files: restoring
// a backup, browsing and the backup before a deploy must not write to or
// read from the linked file.
func TestBotLinksReachNoCoreFile(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	ctx := context.Background()
	base := t.TempDir()
	must(t, os.Chmod(base, 0o711)) //nolint:gosec // the production layout of /data/core
	const uid = 12346
	core := filepath.Join(base, "core.db")
	db, err := sql.Open("sqlite", "file:"+core)
	must(t, err)
	_, err = db.Exec(`CREATE TABLE secrets (v TEXT); INSERT INTO secrets VALUES ('master')`)
	must(t, err)
	must(t, db.Close())
	must(t, os.Chmod(core, 0o600))
	before, err := os.ReadFile(core) //nolint:gosec // test file
	must(t, err)

	// As in /data/core: the bot passes through, lists nothing; backups are root's.
	must(t, os.Chmod(filepath.Dir(base), 0o711))          //nolint:gosec // the production layout of /data/core
	must(t, os.Mkdir(filepath.Join(base, "bots"), 0o711)) //nolint:gosec // the production layout of /data/core
	botDir := filepath.Join(base, "bots", "1")
	must(t, os.Mkdir(botDir, 0o770)) //nolint:gosec // the production layout of /data/core
	must(t, os.Chown(botDir, 0, uid))
	must(t, os.Chmod(botDir, 0o1770)) //nolint:gosec // the bot directory layout
	botDB := filepath.Join(botDir, "bot.db")
	backupDir := filepath.Join(base, "backups", "bots", "1")
	backups := &Backups{B: &Browser{Path: func(int64) string { return botDB }, UID: func(int64) int { return uid }},
		Dir: func(int64) string { return backupDir }}
	must(t, os.MkdirAll(backupDir, 0o700))
	must(t, os.WriteFile(filepath.Join(backupDir, "old.db"), []byte("backup"), 0o600))

	// Restore: bot.db.restore is a link to core.db.
	must(t, os.Symlink(core, botDB+".restore"))
	must(t, backups.Restore(1, "old.db", nil))
	if after, _ := os.ReadFile(core); string(after) != string(before) { //nolint:gosec // test file
		t.Fatal("restore wrote through the link into core.db")
	}

	// Browsing: bot.db is a link to core.db.
	must(t, os.Remove(botDB))
	must(t, os.Symlink(core, botDB))
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
	must(t, os.Remove(botDB))
	must(t, withIdentity(uid, func() error {
		own, err := sql.Open("sqlite", "file:"+botDB)
		if err != nil {
			return err
		}
		if _, err = own.Exec(`CREATE TABLE orders (id INTEGER); INSERT INTO orders VALUES (1)`); err != nil {
			return err
		}
		return own.Close()
	}))
	b, err := backups.Make(ctx, 1, "deploy")
	if err != nil || !b.OK {
		t.Fatalf("own database not backed up: %+v %v", b, err)
	}
	fi, err := os.Lstat(filepath.Join(backupDir, b.Name))
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v %v", fi, err)
	}
}
