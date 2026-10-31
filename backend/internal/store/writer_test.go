package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

var logMigrations = fstest.MapFS{
	"0001_init.sql": {Data: []byte(`
CREATE TABLE log (n INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT NOT NULL);
CREATE TABLE seen (k TEXT PRIMARY KEY, v INTEGER NOT NULL);`)},
}

func openLog(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "w.db"), logMigrations)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func insert(v string) Op {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO log (v) VALUES (?)`, v)
		return err
	}
}

func values(t *testing.T, db *DB) []string {
	rows, err := db.R.Query(`SELECT v FROM log ORDER BY n`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		rows.Scan(&v)
		out = append(out, v)
	}
	return out
}

func TestWriterOrder(t *testing.T) {
	db := openLog(t)
	w := NewWriter(db, time.Hour, nil)
	ctx := context.Background()
	w.Later("", insert("a"))
	w.Later("", insert("b"))
	if err := w.Now(ctx, insert("c")); err != nil {
		t.Fatal(err)
	}
	w.Later("", insert("d"))
	w.Close()
	got := fmt.Sprint(values(t, db))
	if got != "[a b c d]" {
		t.Fatalf("order = %s", got)
	}
}

func TestWriterCoalesce(t *testing.T) {
	db := openLog(t)
	w := NewWriter(db, time.Hour, nil)
	for i := range 100 {
		w.Later("user:1", func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO seen (k, v) VALUES ('u1', ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v`, i)
			return err
		})
	}
	if s := w.Stats(); s.Queued != 1 {
		// Stats are updated by the loop; give it a moment.
		time.Sleep(50 * time.Millisecond)
		if s = w.Stats(); s.Queued != 1 {
			t.Errorf("queued = %d", s.Queued)
		}
	}
	w.Close()
	var v int
	db.R.QueryRow(`SELECT v FROM seen WHERE k = 'u1'`).Scan(&v)
	if v != 99 {
		t.Fatalf("v = %d", v)
	}
}

func TestWriterFlushOnClose(t *testing.T) {
	db := openLog(t)
	w := NewWriter(db, time.Hour, nil)
	for i := range 50 {
		w.Later("", insert(fmt.Sprint(i)))
	}
	w.Close()
	if n := len(values(t, db)); n != 50 {
		t.Fatalf("rows = %d", n)
	}
	if err := w.Later("", insert("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("want ErrClosed, got %v", err)
	}
}

func TestWriterInterval(t *testing.T) {
	db := openLog(t)
	w := NewWriter(db, 20*time.Millisecond, nil)
	defer w.Close()
	w.Later("", insert("a"))
	deadline := time.Now().Add(2 * time.Second)
	for len(values(t, db)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("batch was not flushed by the ticker")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWriterBadOpSkipped(t *testing.T) {
	db := openLog(t)
	w := NewWriter(db, time.Hour, nil)
	w.Later("", insert("a"))
	w.Later("", func(tx *sql.Tx) error { _, err := tx.Exec(`INSERT INTO nope VALUES (1)`); return err })
	w.Later("", func(*sql.Tx) error { panic("boom") })
	w.Later("", insert("b"))
	err := w.Now(context.Background(), func(tx *sql.Tx) error { return errors.New("now failed") })
	if err == nil || err.Error() != "now failed" {
		t.Fatalf("now error = %v", err)
	}
	w.Close()
	if got := fmt.Sprint(values(t, db)); got != "[a b]" {
		t.Fatalf("rows = %s", got)
	}
}

func TestWriterParallel(t *testing.T) {
	db := openLog(t)
	w := NewWriter(db, 5*time.Millisecond, nil)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 1000)
	for g := range 10 {
		wg.Go(func() {
			for i := range 50 {
				if i%5 == 0 {
					errs <- w.Now(ctx, insert(fmt.Sprintf("now-%d-%d", g, i)))
				} else {
					errs <- w.Later("", insert(fmt.Sprintf("later-%d-%d", g, i)))
				}
			}
		})
	}
	// Readers run at the same time as the writer.
	stop := make(chan struct{})
	var rg sync.WaitGroup
	rg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				var n int
				if err := db.R.QueryRow(`SELECT COUNT(*) FROM log`).Scan(&n); err != nil {
					errs <- err
				}
			}
		}
	})
	wg.Wait()
	close(stop)
	rg.Wait()
	w.Close()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err) // includes "database is locked"
		}
	}
	if n := len(values(t, db)); n != 500 {
		t.Fatalf("rows = %d", n)
	}
}
