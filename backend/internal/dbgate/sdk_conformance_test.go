package dbgate

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
)

// The exact same SQL suite exercises the public driver in both modes. It
// lives in the core module to keep the standalone SDK independent of core.
func TestSDKDatabaseConformance(t *testing.T) {
	for _, mode := range []string{"file", "socket"} {
		t.Run(mode, func(t *testing.T) {
			var db *sql.DB
			if mode == "socket" {
				db, _ = gatewayDB(t)
			} else {
				t.Setenv("TGPANEL_SDK", "")
				var err error
				db, err = sql.Open("tgpanel", "file:"+filepath.Join(t.TempDir(), "bot.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
			}
			db.SetMaxOpenConns(12)
			db.SetMaxIdleConns(12)
			if err := db.Ping(); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE types(n, i, r, t, b, empty_blob, null_blob)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO types VALUES (?, ?, ?, ?, ?, ?, ?)`, nil, int64(-9223372036854775807), 1.25, "текст\x00text", []byte{0, 1, 255}, []byte{}, []byte(nil)); err != nil {
				t.Fatal(err)
			}
			var n, nullBlob any
			var integer int64
			var real float64
			var text string
			var blob, empty []byte
			if err := db.QueryRow(`SELECT * FROM types`).Scan(&n, &integer, &real, &text, &blob, &empty, &nullBlob); err != nil {
				t.Fatal(err)
			}
			if n != nil || nullBlob != nil || integer != -9223372036854775807 || real != 1.25 || text != "текст\x00text" || !bytes.Equal(blob, []byte{0, 1, 255}) || len(empty) != 0 {
				t.Fatalf("storage classes changed: %#v %d %g %q %v %v %#v", n, integer, real, text, blob, empty, nullBlob)
			}
			var emptyValue any
			if err := db.QueryRow(`SELECT empty_blob FROM types`).Scan(&emptyValue); err != nil {
				t.Fatal(err)
			}
			if _, ok := emptyValue.([]byte); !ok {
				t.Fatalf("empty BLOB became NULL: %#v", emptyValue)
			}
			if _, err := db.Exec(`CREATE TABLE items(id INTEGER PRIMARY KEY, value TEXT)`); err != nil {
				t.Fatal(err)
			}
			prepared, err := db.Prepare(`INSERT INTO items(value) VALUES (:value)`)
			if err != nil {
				t.Fatal(err)
			}
			defer prepared.Close()
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Stmt(prepared).Exec(sql.Named("value", "committed")); err != nil {
				t.Fatal(err)
			}
			// The transaction must survive the end of the begin RPC handler.
			time.Sleep(time.Millisecond)
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			tx, err = db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`INSERT INTO items(value) VALUES ('rolled back')`); err != nil {
				t.Fatal(err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("transaction: %d %v", count, err)
			}
			if _, err := db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<200) INSERT INTO items(value) SELECT printf('row-%d',x) FROM n`); err != nil {
				t.Fatal(err)
			}
			rows, err := db.Query(`SELECT id,value FROM items ORDER BY id`)
			if err != nil {
				t.Fatal(err)
			}
			seen := 0
			for rows.Next() {
				var id int64
				var value string
				if err := rows.Scan(&id, &value); err != nil {
					t.Fatal(err)
				}
				seen++
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			if seen != 201 {
				t.Fatalf("batched rows: %d", seen)
			}
			selectStmt, err := db.Prepare(`SELECT * FROM items ORDER BY id LIMIT 1`)
			if err != nil {
				t.Fatal(err)
			}
			defer selectStmt.Close()
			if _, err := db.Exec(`ALTER TABLE items ADD COLUMN extra TEXT DEFAULT 'new'`); err != nil {
				t.Fatal(err)
			}
			var id int64
			var value, extra string
			if err := selectStmt.QueryRow().Scan(&id, &value, &extra); err != nil || extra != "new" {
				t.Fatalf("schema change: %q %v", extra, err)
			}
			var wg sync.WaitGroup
			errors := make(chan error, 20)
			for worker := 0; worker < 20; worker++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for j := 0; j < 5; j++ {
						if _, err := prepared.Exec(sql.Named("value", "parallel")); err != nil {
							errors <- err
							return
						}
					}
				}()
			}
			wg.Wait()
			close(errors)
			for err := range errors {
				t.Error(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&count); err != nil || count != 301 {
				t.Fatalf("parallel writes: %d %v", count, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			if err := db.QueryRowContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000000000) SELECT sum(x) FROM n`).Scan(&count); err == nil {
				t.Fatal("query ignored cancellation")
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&count); err != nil || count != 301 {
				t.Fatalf("connection after cancellation: %d %v", count, err)
			}
		})
	}
}

func TestGatewayClosesAbandonedCursor(t *testing.T) {
	db, g := gatewayDB(t)
	// Force more than one batch and leave the result unread.
	rows, err := db.Query(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000) SELECT x FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	if active := g.Snapshot(1).Active; active != 1 {
		t.Fatalf("active: %d", active)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if active := g.Snapshot(1).Active; active != 0 {
		t.Fatalf("cursor leaked: %d", active)
	}
}

func TestGatewayServerQueryTimeout(t *testing.T) {
	db, g := gatewayDB(t)
	g.Timeout = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var n int64
	err := db.QueryRowContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000000000) SELECT sum(x) FROM n`).Scan(&n)
	if err == nil {
		t.Fatal("query ignored core timeout")
	}
	if ctx.Err() != nil {
		t.Fatalf("client deadline was required: %v", err)
	}
	if active := g.Snapshot(1).Active; active != 0 {
		t.Fatalf("timed out query still active: %d", active)
	}
}

func TestGatewayRollbackOnDisconnect(t *testing.T) {
	db, g := gatewayDB(t)
	if _, err := db.Exec(`CREATE TABLE items(id)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO items VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	var conns []*wire.Conn
	for c := range g.sessions {
		conns = append(conns, c)
	}
	g.mu.Unlock()
	for _, c := range conns {
		c.Close()
		g.Disconnect(c)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("transaction moved to another session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var count int
		err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items`).Scan(&count)
		if err == nil {
			if count != 0 {
				t.Fatalf("disconnected transaction committed: %d", count)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(fmt.Errorf("connection did not recover: %w", err))
		case <-time.After(10 * time.Millisecond):
		}
	}
}
