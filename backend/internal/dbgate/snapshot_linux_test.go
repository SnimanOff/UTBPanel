//go:build linux

package dbgate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSnapshotUsesBotFilesystemIdentity(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	dir, err := os.MkdirTemp("", "snapshot-identity-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	os.Chmod(dir, 0o711)
	src := filepath.Join(dir, "source.db")
	dstDir := filepath.Join(dir, "copies")
	os.Mkdir(dstDir, 0o700)
	uid := 10071
	os.Chown(dstDir, uid, uid)
	db, err := sql.Open("sqlite", dsn(src, true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE secret(value); INSERT INTO secret VALUES('root-only')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	os.Chmod(src, 0o600)
	dst := filepath.Join(dstDir, "snapshot.db")
	if err := Snapshot(context.Background(), src, dst, uid); err == nil {
		t.Fatal("root database was readable as a bot")
	}
	if os.Geteuid() != 0 {
		t.Fatal("process identity changed")
	}
	os.Chown(src, uid, uid)
	if err := Snapshot(context.Background(), src, dst, uid); err != nil {
		t.Fatal("bot cannot snapshot its own database", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Sys().(*syscall.Stat_t).Uid != uint32(uid) {
		t.Fatal("snapshot not owned by bot")
	}
}
