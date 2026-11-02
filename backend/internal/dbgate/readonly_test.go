package dbgate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// query_db is read-only by its connection, not by the text: a query that
// tries to switch the mode off, attach another file or copy the database
// must not change anything (the text may come from an AI that read a log).
func TestReadOnlyConnectionNotText(t *testing.T) {
	ctx := context.Background()
	b, path := newBrowser(t)
	dir := filepath.Dir(path)
	before, _ := os.ReadFile(path)
	for _, q := range []string{
		`PRAGMA query_only = 0; DELETE FROM orders`,
		`PRAGMA query_only = 0`,
		`DELETE FROM orders`,
		`ATTACH DATABASE '` + filepath.Join(dir, "x.db") + `' AS x`,
		`VACUUM INTO '` + filepath.Join(dir, "copy.db") + `'`,
		`CREATE TABLE evil (a)`,
		`SELECT 1; DROP TABLE orders`,
	} {
		b.Query(ctx, 1, q, false)
	}
	r, err := b.Query(ctx, 1, `SELECT COUNT(*) FROM orders`, false)
	if err != nil || r.Rows[0][0] != int64(3) {
		t.Fatalf("rows changed: %+v %v", r, err)
	}
	for _, name := range []string{"x.db", "copy.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s was created", name)
		}
	}
	if info, _ := b.Tables(ctx, 1); len(info.Tables) != 2 {
		t.Fatalf("tables %+v", info.Tables)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("the database file changed")
	}
	// The schema is the bot's: its views and triggers are not trusted.
	for _, write := range []bool{false, true} {
		r, err := b.Query(ctx, 1, `PRAGMA trusted_schema`, write)
		if err != nil || len(r.Rows) != 1 || r.Rows[0][0] != int64(0) {
			t.Fatalf("trusted_schema (write=%v): %+v %v", write, r.Rows, err)
		}
	}
}
