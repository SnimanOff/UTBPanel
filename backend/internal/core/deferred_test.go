package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/edge"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

func TestBotUnavailable(t *testing.T) {
	old := WaitForBot
	WaitForBot = 200 * time.Millisecond
	t.Cleanup(func() { WaitForBot = old })
	ctx := context.Background()
	c := newCore(t)
	withTelegram(t, c)
	b, _ := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:good"})

	msg := `{"update_id":1,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"text":"hi"}}`
	start := time.Now()
	rep := c.Pipe.Handle(ctx, b.ID, []byte(msg), time.Now())
	if time.Since(start) < WaitForBot || !strings.Contains(string(rep.Body), DefaultUnavailable) {
		t.Fatalf("%s after %v", rep.Body, time.Since(start))
	}
	pay := `{"update_id":2,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"successful_payment":{"total_amount":1}}}`
	c.Pipe.Handle(ctx, b.ID, []byte(pay), time.Now())
	if c.DeferredCount(ctx, b.ID) != 1 {
		t.Fatal("payment not deferred while the bot is down")
	}
}

func TestEdgeQueueHandOver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newCore(t)
	withTelegram(t, c)
	b, _ := c.CreateBot(ctx, CreateBot{Name: "Echo", Token: "111:good"})
	go c.Run(ctx)
	var conn *wire.Conn
	for range 100 {
		cc, err := wire.Dial(ctx, wire.CoreSock(c.Cfg.RunDir), nil, wire.Hello{Proto: wire.ProtoEdgeCore, Version: 1}, nil)
		if err == nil {
			conn = cc
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("no core socket")
	}
	defer conn.Close()
	d := edge.Deferred{BotID: b.ID, Updates: [][]byte{[]byte(`{"update_id":7}`), []byte(`{"update_id":8}`)}}
	if err := conn.Call(ctx, edge.MethodDeferred, d, nil); err != nil {
		t.Fatal(err)
	}
	if n := c.DeferredCount(ctx, b.ID); n != 2 {
		t.Fatalf("deferred = %d", n)
	}
}
