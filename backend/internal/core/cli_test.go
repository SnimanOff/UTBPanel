package core

import (
	"context"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
)

// CLI commands reach the core over its socket and land in the journal
// with the source "cli".
func TestCLIOverSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newCore(t)
	withTelegram(t, c)
	b, err := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:good"})
	if err != nil {
		t.Fatal(err)
	}
	go c.Run(ctx)
	var conn interface {
		Call(context.Context, string, any, any) error
		Close() error
	}
	for range 100 {
		if cc, err := DialCLI(ctx, c.Cfg); err == nil {
			conn = cc
			break
		}
	}
	if conn == nil {
		t.Fatal("cannot reach the core socket")
	}
	defer conn.Close()
	var bs []Bot
	if err := conn.Call(ctx, "bots.list", struct{}{}, &bs); err != nil || len(bs) != 1 {
		t.Fatalf("%v %v", bs, err)
	}
	// No version uploaded: start fails, but the attempt is journaled.
	actor := journal.Actor{Source: journal.SourceCLI, Name: "ssh"}
	if err := conn.Call(ctx, "bots.start", BotRef{BotID: b.ID, Actor: actor}, nil); err == nil {
		t.Fatal("start without a version succeeded")
	}
	es, _ := c.Journal.Since(ctx, 0, 10)
	last := es[len(es)-1]
	if last.Actor.Source != journal.SourceCLI || last.Command != "bots.start" || last.Result == "ok" {
		t.Fatalf("journal = %+v", last)
	}
}
