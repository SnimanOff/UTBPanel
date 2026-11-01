package sdk

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
)

func TestAutonomous(t *testing.T) {
	t.Setenv("TGPANEL_SDK", "")
	k := New()
	defer k.Close()
	if err := k.Ready(); err != nil {
		t.Fatal(err)
	}
	if k.Configured() || k.Connected() {
		t.Fatal("unexpected connection")
	}
	if err := k.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := k.Call(context.Background(), "anything", nil, nil); !errors.Is(err, ErrNotConnected) {
		t.Fatal(err)
	}
}

func TestReconnectAndModules(t *testing.T) {
	dir, err := os.MkdirTemp("", "sdk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	t.Setenv("TGPANEL_SDK", "unix://"+path)
	t.Setenv("TGPANEL_CANARY", "1")
	registrations := make(chan Registration, 8)
	mux := wire.NewMux()
	mux.Handle("sdk.register", wire.Func(func(_ context.Context, _ *wire.Conn, in Registration) (any, error) {
		registrations <- in
		return nil, nil
	}))
	srv := &wire.Server{Mux: mux, Hello: wire.Hello{Proto: Protocol, Version: ProtocolVersion}}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go srv.Serve(ln)
	k := New(WithoutReady(), WithModules("metrics"))
	defer k.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := k.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	r := <-registrations
	if r.Version != Version || r.Instance != "canary" || len(r.Modules) != 1 || r.Modules[0] != "metrics" {
		t.Fatalf("%+v", r)
	}
	if err := k.Use("db"); err != nil {
		t.Fatal(err)
	}
	if r = <-registrations; len(r.Modules) != 2 {
		t.Fatalf("%+v", r)
	}
	for _, c := range srv.Conns() {
		c.Close()
	}
	select {
	case r = <-registrations:
		if len(r.Modules) != 2 {
			t.Fatalf("lost modules: %+v", r)
		}
	case <-ctx.Done():
		t.Fatal("did not reconnect")
	}
}

func TestUnavailableDoesNotBlockNew(t *testing.T) {
	t.Setenv("TGPANEL_SDK", "unix://"+filepath.Join(t.TempDir(), "absent.sock"))
	k := New(WithConnectTimeout(20 * time.Millisecond))
	defer k.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if !k.Configured() || k.Connected() {
		t.Fatal("incorrect state")
	}
	if err := k.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
