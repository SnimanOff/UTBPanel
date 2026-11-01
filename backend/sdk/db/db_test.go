package db

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk"
	"github.com/SnimanOff/UTBPanel/backend/sdk/db/protocol"
	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
)

func TestAutonomousDatabase(t *testing.T) {
	t.Setenv("TGPANEL_SDK", "")
	t.Setenv("DATABASE_URL", "file:"+filepath.Join(t.TempDir(), "bot.db"))
	k := sdk.New()
	defer k.Close()
	db, err := Open(k)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT); INSERT INTO items(name) VALUES ('tea')`); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM items`).Scan(&name); err != nil || name != "tea" {
		t.Fatalf("%q %v", name, err)
	}
}

func TestMissingCoreNeverOpensLocalFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-exist.db")
	t.Setenv("TGPANEL_SDK", "unix://"+filepath.Join(t.TempDir(), "absent.sock"))
	db, err := sql.Open("tgpanel", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := db.PingContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("local database opened: %v", err)
	}
}

func TestInFlightWriteIsNotRetried(t *testing.T) {
	dir, err := os.MkdirTemp("", "dbcl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var writes atomic.Int64
	mux := wire.NewMux()
	mux.Handle("sdk.register", func(context.Context, *wire.Conn, wire.Raw) (any, error) { return nil, nil })
	mux.Handle("db.open", func(context.Context, *wire.Conn, wire.Raw) (any, error) {
		return protocol.Opened{ID: "conn", Version: protocol.Version}, nil
	})
	mux.Handle("db.exec", func(_ context.Context, c *wire.Conn, _ wire.Raw) (any, error) {
		writes.Add(1)
		c.Close()
		return protocol.Result{Affected: 1}, nil
	})
	srv := &wire.Server{Mux: mux, Hello: wire.Hello{Proto: sdk.Protocol, Version: sdk.ProtocolVersion}}
	go srv.Serve(ln)
	t.Setenv("TGPANEL_SDK", "unix://"+path)
	db, err := sql.Open("tgpanel", "file:unused.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `INSERT INTO orders DEFAULT VALUES`); err == nil {
		t.Fatal("lost response must report an error")
	}
	if writes.Load() != 1 {
		t.Fatalf("write repeated %d times", writes.Load())
	}
}
