package sdkhost

import (
	"context"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk"
)

func TestReadyReplayAndInstanceReset(t *testing.T) {
	h := newHost(t)
	if err := h.Start(1, "", testUID()); err != nil {
		t.Fatal(err)
	}
	if err := h.Start(1, "canary", testUID()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TGPANEL_SDK", "unix://"+h.Path(1, ""))
	t.Setenv("TGPANEL_CANARY", "")
	k := sdk.New()
	defer k.Close()
	if uses, ready := h.Readiness(1, ""); !uses || ready {
		t.Fatalf("requirement must be registered before New returns: %v %v", uses, ready)
	}
	if err := k.Ready(); err != nil {
		t.Fatal(err)
	}
	if _, ready := h.Readiness(1, ""); !ready {
		t.Fatal("signal not received")
	}
	if _, ready := h.Readiness(1, "canary"); ready {
		t.Fatal("main signal made canary ready")
	}
	// A new endpoint does not inherit ready state. The same live kit must
	// register and replay its signal on the new connection.
	if err := h.Start(1, "", testUID()); err != nil {
		t.Fatal(err)
	}
	if _, ready := h.Readiness(1, ""); ready {
		t.Fatal("ready leaked across endpoints")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if _, ready := h.Readiness(1, ""); ready {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("ready not replayed")
		case <-time.After(time.Millisecond):
		}
	}
}
