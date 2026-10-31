package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"
)

var testMigrations = fstest.MapFS{
	"0001_init.sql": {Data: []byte(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL);`)},
	"0002_more.sql": {Data: []byte(`ALTER TABLE items ADD COLUMN note TEXT;`)},
	"readme.txt":    {Data: []byte(`ignored`)},
}

func TestMigrations(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(ctx, file, testMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.W.Exec(`INSERT INTO items (name, note) VALUES ('a', 'b')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Second start: nothing to apply, data intact.
	db, err = Open(ctx, file, testMigrations)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	db.R.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows = %d", n)
	}
	var mode string
	db.R.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Errorf("journal_mode = %s", mode)
	}
	if _, err := db.R.Exec(`INSERT INTO items (name) VALUES ('x')`); err == nil {
		t.Error("read pool accepted a write")
	}
	db.Close()

	// Older program: only the first migration known.
	old := fstest.MapFS{"0001_init.sql": testMigrations["0001_init.sql"]}
	if _, err := Open(ctx, file, old); !errors.Is(err, ErrTooNew) {
		t.Fatalf("want ErrTooNew, got %v", err)
	}
}

func TestBadMigrationRollsBack(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "t.db")
	bad := fstest.MapFS{
		"0001_init.sql": testMigrations["0001_init.sql"],
		"0002_bad.sql":  {Data: []byte(`ALTER TABLE nope ADD COLUMN x;`)},
	}
	if _, err := Open(ctx, file, bad); err == nil {
		t.Fatal("want error")
	}
	db, err := Open(ctx, file, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	db.R.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name = 'items'`).Scan(&n)
	if n != 0 {
		t.Fatal("first migration should be rolled back together with the failed one")
	}
}

func TestGapInNumbers(t *testing.T) {
	gap := fstest.MapFS{"0002_x.sql": {Data: []byte(`SELECT 1;`)}}
	if _, err := readMigrations(gap); err == nil {
		t.Fatal("want error for gap")
	}
}

func TestTx(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"), testMigrations)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	boom := errors.New("boom")
	err = db.Tx(ctx, func(tx *sql.Tx) error {
		tx.Exec(`INSERT INTO items (name) VALUES ('a')`)
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	var n int
	db.R.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n)
	if n != 0 {
		t.Fatal("rollback failed")
	}
}
