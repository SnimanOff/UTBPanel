package sdkhost

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk"
	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
)

func newHost(t *testing.T) *Host {
	t.Helper()
	dir, err := os.MkdirTemp("", "sdh")
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: filepath.Join(dir, "sdk")}
	t.Cleanup(func() { h.Close(); os.RemoveAll(dir) })
	return h
}

func testUID() int {
	if runtime.GOOS == "linux" {
		return os.Geteuid()
	}
	return -1
}

func TestRegistrationAndIsolation(t *testing.T) {
	h := newHost(t)
	if err := h.Start(1, "", testUID()); err != nil {
		t.Fatal(err)
	}
	if err := h.Start(1, "canary", testUID()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TGPANEL_SDK", "unix://"+h.Path(1, ""))
	t.Setenv("TGPANEL_CANARY", "")
	k := sdk.New(sdk.WithModules("metrics"))
	defer k.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := k.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	i := h.Info(1, "")
	if i == nil || !i.Connected || i.Version != sdk.Version {
		t.Fatalf("%+v", i)
	}
	if h.Info(1, "canary") != nil || h.Info(2, "") != nil {
		t.Fatal("registration escaped its instance")
	}
	if err := k.Call(ctx, "bots.list", nil, nil); err == nil {
		t.Fatal("bot can call core methods")
	}
	i.Modules[0] = "tampered"
	if h.Info(1, "").Modules[0] != "metrics" {
		t.Fatal("Info aliases internal state")
	}
	k.Close()
	for h.Info(1, "").Connected {
		select {
		case <-ctx.Done():
			t.Fatal("not disconnected")
		case <-time.After(time.Millisecond):
		}
	}
	if err := h.Start(1, "", testUID()); err != nil {
		t.Fatal(err)
	}
	if h.Info(1, "") != nil {
		t.Fatal("old process registration survived restart")
	}
}

func TestInvalidRegistration(t *testing.T) {
	h := newHost(t)
	if err := h.Start(1, "", testUID()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := wire.Dial(ctx, h.Path(1, ""), nil, wire.Hello{Proto: sdk.Protocol, Version: sdk.ProtocolVersion}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, r := range []sdk.Registration{{Version: "1", Instance: "canary"}, {Version: "1", Modules: []string{"admin"}}, {}} {
		if err := c.Call(ctx, "sdk.register", r, nil); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	if h.Info(1, "") != nil {
		t.Fatal("invalid registration became visible")
	}
	if runtime.GOOS == "linux" {
		for path, mode := range map[string]os.FileMode{h.Dir: 0o711, h.Path(1, ""): 0o600} {
			st, err := os.Stat(path)
			if err != nil || st.Mode().Perm() != mode {
				t.Fatalf("mode of %s: %v %v", path, st, err)
			}
		}
	}
}
