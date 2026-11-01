package dbgate

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk"
	"github.com/SnimanOff/UTBPanel/backend/sdk/db/protocol"
	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
)

func TestDatabaseProtocolV1OnNewerTransport(t *testing.T) {
	dir, err := os.MkdirTemp("", "dbv1")
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
	mux := wire.NewMux()
	g := &Gateway{Path: func(int64) string { return filepath.Join(dir, "bot.db") }}
	g.Register(mux, 1)
	// Core transport v3 continues to accept a v1 SDK and DB envelope.
	srv := &wire.Server{Mux: mux, Hello: wire.Hello{Proto: sdk.Protocol, Version: 3, MinVersion: 1}, OnDisconnect: func(c *wire.Conn, _ error) { g.Disconnect(c) }}
	go srv.Serve(ln)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := wire.Dial(ctx, path, nil, wire.Hello{Proto: sdk.Protocol, Version: 1, MinVersion: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Version() != 1 {
		t.Fatal(c.Version())
	}
	var opened protocol.Opened
	if err := c.Call(ctx, "db.open", protocol.Open{Version: 1}, &opened); err != nil {
		t.Fatal(err)
	}
	var batch protocol.Batch
	if err := c.Call(ctx, "db.query", protocol.Request{Conn: opened.ID, SQL: `SELECT 42`}, &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Rows) != 1 || batch.Rows[0][0].Integer != 42 || !batch.Done {
		t.Fatalf("%+v", batch)
	}
	if err := c.Call(ctx, "db.open", protocol.Open{Version: 999}, nil); err == nil {
		t.Fatal("accepted incompatible DB protocol")
	}
	if err := c.Call(ctx, "db.close", protocol.Request{Conn: opened.ID}, nil); err != nil {
		t.Fatal(err)
	}
}
