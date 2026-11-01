package dbgate

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSnapshotWithWALAndSpecialPath(t *testing.T) {
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "source #1.db")
	dst := filepath.Join(t.TempDir(), "copy #2.db")
	db, err := sql.Open("sqlite", dsn(src, true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE items(n); INSERT INTO items VALUES(42)`); err != nil {
		t.Fatal(err)
	}
	if err := Snapshot(ctx, src, dst, -1); err != nil {
		t.Fatal(err)
	}
	copy, err := sql.Open("sqlite", dsn(dst, false))
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	var n int
	if err := copy.QueryRow(`SELECT n FROM items`).Scan(&n); err != nil || n != 42 {
		t.Fatal(n, err)
	}
}
