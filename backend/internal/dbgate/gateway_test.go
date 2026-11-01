package dbgate

import (
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk"
	_ "github.com/SnimanOff/UTBPanel/backend/sdk/db"
	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
)

func gatewayDB(t *testing.T) (*sql.DB, *Gateway) {
	t.Helper()
	dir, err := os.MkdirTemp("", "dbgw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "sdk.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	mux := wire.NewMux()
	mux.Handle("sdk.register", func(context.Context, *wire.Conn, wire.Raw) (any, error) { return nil, nil })
	g := &Gateway{Path: func(int64) string { return filepath.Join(dir, "bot.db") }, SlowThreshold: time.Nanosecond}
	g.Register(mux, 1)
	srv := &wire.Server{Mux: mux, Hello: wire.Hello{Proto: sdk.Protocol, Version: sdk.ProtocolVersion}, OnDisconnect: func(c *wire.Conn, _ error) { g.Disconnect(c) }}
	go srv.Serve(ln)
	t.Cleanup(func() {
		ln.Close()
		for _, c := range srv.Conns() {
			c.Close()
			g.Disconnect(c)
		}
	})
	t.Setenv("TGPANEL_SDK", "unix://"+path)
	db, err := sql.Open("tgpanel", "file:this-client-path-is-ignored.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, g
}

func TestGatewayRoundtripAndMetrics(t *testing.T) {
	db, g := gatewayDB(t)
	if _, err := db.Exec(`CREATE TABLE items(id INTEGER PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO items(value) VALUES (?)`, "secret-token"); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM items WHERE value='secret-token'`).Scan(&value); err != nil || value != "secret-token" {
		t.Fatalf("%s %v", value, err)
	}
	stats := g.Snapshot(1)
	if stats.Active != 0 || stats.Counts["SELECT"] != 1 || stats.Counts["INSERT"] != 1 || len(stats.Slow) == 0 {
		t.Fatalf("%+v", stats)
	}
	for _, q := range stats.Slow {
		if strings.Contains(q.SQL, "secret-token") {
			t.Fatal("literal leaked into metrics")
		}
	}
	if _, err := db.Exec(`ATTACH DATABASE ':memory:' AS other`); err == nil {
		t.Fatal("ATTACH allowed")
	}
	if _, err := db.Exec(`VACUUM INTO 'outside.db'`); err == nil {
		t.Fatal("VACUUM INTO allowed")
	}
}

func TestSQLSummary(t *testing.T) {
	for _, text := range []string{`SELECT 'secret', 123.45, x'1234' -- secret`, `/* secret */ WITH x AS (SELECT 'secret') SELECT 123 FROM x`, `SELECT "secret", [secret], ` + "`secret`"} {
		kind, safe := summarizeSQL(text)
		if kind != "SELECT" || strings.Contains(safe, "secret") || strings.Contains(safe, "123") {
			t.Fatalf("%q -> %s %s", text, kind, safe)
		}
	}
	kind, _ := summarizeSQL(`CREATE TABLE x AS SELECT 1`)
	if kind != "other" {
		t.Fatal(kind)
	}
}
