package runner

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSDKReadyOverridesOpenPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var signaled atomic.Bool
	r := New(Config{SDKReadyTimeout: time.Second, Hooks: Hooks{Readiness: func(Bot) (bool, bool) { return true, signaled.Load() }}})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- r.waitReady(ctx, Bot{Port: ln.Addr().(*net.TCPAddr).Port}, make(chan struct{})) }()
	select {
	case err := <-result:
		t.Fatalf("open port bypassed SDK ready: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	signaled.Store(true)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestSDKReadyTimeoutCancellationAndExit(t *testing.T) {
	r := New(Config{SDKReadyTimeout: 30 * time.Millisecond, Hooks: Hooks{Readiness: func(Bot) (bool, bool) { return true, false }}})
	err := r.waitReady(context.Background(), Bot{}, make(chan struct{}))
	if err == nil || !strings.Contains(err.Error(), "SDK ready") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.waitReady(ctx, Bot{}, make(chan struct{})); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	done := make(chan struct{})
	close(done)
	if err := r.waitReady(context.Background(), Bot{}, done); !errors.Is(err, ErrExited) {
		t.Fatal(err)
	}
}
