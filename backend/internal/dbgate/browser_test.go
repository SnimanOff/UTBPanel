package dbgate

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func newBrowser(t *testing.T) (*Browser, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bot.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE orders (id INTEGER PRIMARY KEY, user INTEGER NOT NULL, item TEXT, data BLOB);
		INSERT INTO orders (user, item, data) VALUES (1, 'tea', x'0102'), (2, 'coffee', NULL), (3, 'cake, big', NULL);
		CREATE VIEW v AS SELECT item FROM orders;`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	return &Browser{Path: func(int64) string { return path }}, path
}

func TestTablesAndRows(t *testing.T) {
	ctx := context.Background()
	b, _ := newBrowser(t)
	info, err := b.Tables(ctx, 1)
	if err != nil || len(info.Tables) != 2 || info.Size == 0 {
		t.Fatalf("%+v %v", info, err)
	}
	o := info.Tables[0]
	if o.Name != "orders" || o.Rows != 3 || len(o.Schema) != 4 || !o.Schema[0].PK || !o.Schema[1].NotNull {
		t.Fatalf("orders = %+v", o)
	}
	r, err := b.Rows(ctx, 1, "orders", 2, 0)
	if err != nil || len(r.Rows) != 2 || r.Rows[0][3] != "x'0102'" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := b.Rows(ctx, 1, `orders"; DROP TABLE orders; --`, 2, 0); err == nil {
		t.Fatal("bad table name accepted")
	}
}

func TestReadOnlyConsole(t *testing.T) {
	ctx := context.Background()
	b, _ := newBrowser(t)
	if _, err := b.Query(ctx, 1, `DELETE FROM orders`, false); err == nil {
		t.Fatal("write through the read-only console")
	}
	// Tricks with the text do not help: the connection is read-only.
	if _, err := b.Query(ctx, 1, `WITH x AS (SELECT 1) DELETE FROM orders`, false); err == nil {
		t.Fatal("write hidden in WITH")
	}
	r, err := b.Query(ctx, 1, `SELECT COUNT(*) AS n FROM orders`, false)
	if err != nil || r.Rows[0][0] != int64(3) {
		t.Fatalf("%+v %v", r, err)
	}
	w, err := b.Query(ctx, 1, `DELETE FROM orders WHERE user = 1`, true)
	if err != nil || w.Affected != 1 {
		t.Fatalf("write: %+v %v", w, err)
	}
}

func TestCSV(t *testing.T) {
	b, _ := newBrowser(t)
	var buf bytes.Buffer
	if err := b.CSV(context.Background(), 1, "orders", "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `3,3,"cake, big",`) || !strings.HasPrefix(buf.String(), "id,user,item,data") {
		t.Fatalf("%s", buf.String())
	}
}

func TestNoDB(t *testing.T) {
	b := &Browser{Path: func(int64) string { return filepath.Join(t.TempDir(), "none.db") }}
	if _, err := b.Tables(context.Background(), 1); err != ErrNoDB {
		t.Fatalf("got %v", err)
	}
}
