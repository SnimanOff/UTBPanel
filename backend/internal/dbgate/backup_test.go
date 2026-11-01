package dbgate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupRestore(t *testing.T) {
	ctx := context.Background()
	b, path := newBrowser(t)
	k := &Backups{B: b, Dir: func(int64) string { return filepath.Join(filepath.Dir(path), "backups") }}
	before, _ := b.SchemaVersion(ctx, 1)
	bk, err := k.Make(ctx, 1, "deploy")
	if err != nil || !bk.OK || bk.SchemaVersion != before || bk.Size == 0 {
		t.Fatalf("%+v %v", bk, err)
	}
	// The bot changes the schema and the data.
	db, _ := sql.Open("sqlite", "file:"+path)
	db.Exec(`ALTER TABLE orders ADD COLUMN price INTEGER; DELETE FROM orders`)
	db.Close()
	after, _ := b.SchemaVersion(ctx, 1)
	if after == before {
		t.Fatal("schema_version did not change")
	}
	if err := k.Restore(1, bk.Name, nil); err != nil {
		t.Fatal(err)
	}
	r, _ := b.Query(ctx, 1, `SELECT COUNT(*) FROM orders`, false)
	if r.Rows[0][0] != int64(3) {
		t.Fatalf("rows after restore = %v", r.Rows[0][0])
	}
	if _, err := b.Query(ctx, 1, `SELECT price FROM orders`, false); err == nil {
		t.Fatal("schema not restored: the new column is still there")
	}
	if _, err := k.Path(1, "../bot.db"); err == nil {
		t.Fatal("path traversal")
	}
}

func TestDamagedBackupNotCounted(t *testing.T) {
	ctx := context.Background()
	old := Keep
	Keep = 2
	t.Cleanup(func() { Keep = old })
	b, path := newBrowser(t)
	dir := filepath.Join(filepath.Dir(path), "backups")
	k := &Backups{B: b, Dir: func(int64) string { return dir }}
	for range 3 {
		if _, err := k.Make(ctx, 1, "daily"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	bs, _ := k.List(1)
	if len(bs) != 2 {
		t.Fatalf("kept %d", len(bs))
	}
	// Damage the newest copy and check it again.
	p, _ := k.Path(1, bs[0].Name)
	f, _ := os.OpenFile(p, os.O_RDWR, 0)
	f.WriteAt(make([]byte, 4096), 4096)
	f.Close()
	if err := Integrity(ctx, p, false); err == nil {
		t.Fatal("damage not detected")
	}
}
