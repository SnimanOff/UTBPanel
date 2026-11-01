package core

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk/flags"
)

func testSDKUID() int {
	if runtime.GOOS == "linux" {
		return os.Geteuid()
	}
	return -1
}

func TestSDKFlagsLiveAndBeforeReady(t *testing.T) {
	c := taskCore(t)
	k := taskKit(t, c, "")
	b := k.Flag("checkout", false, "new checkout")
	i := k.IntFlag("retries", 2, "retry count")
	s := k.StringFlag("label", "default", "")
	d := k.DurationFlag("timeout", time.Second, "")
	ctx := context.Background()
	for name, value := range map[string]flags.Value{"checkout": {Kind: "bool", Bool: true}, "retries": {Kind: "int", Int: 5}, "label": {Kind: "string", Text: "new"}, "timeout": {Kind: "string", Text: "2m"}} {
		if err := c.SDKFlags.Set(ctx, 1, name, value, false); err != nil {
			t.Fatal(err)
		}
	}
	if !b.On() || i.Value() != 5 || s.Value() != "new" || d.Value() != 2*time.Minute {
		t.Fatal("live update failed")
	}
	if err := c.SDKFlags.Set(ctx, 1, "retries", flags.Value{Kind: "string", Text: "wrong"}, false); err == nil {
		t.Fatal("wrong type accepted")
	}
	if err := c.SDKFlags.Set(ctx, 1, "label", flags.Value{}, true); err != nil {
		t.Fatal(err)
	}
	if s.Value() != "default" {
		t.Fatal(s.Value())
	}
	marks, err := c.SDKFlags.Marks(ctx, 1, 0)
	if err != nil || len(marks) != 5 {
		t.Fatalf("marks: %+v %v", marks, err)
	}
	k.Close()
	k2 := taskKit(t, c, "")
	restored := k2.Flag("checkout", false, "")
	if !restored.On() {
		t.Fatal("persisted override not loaded during declaration")
	}
	if _, ready := c.SDK.Readiness(1, ""); ready {
		t.Fatal("bot ready too early")
	}
	if err := k2.Ready(); err != nil {
		t.Fatal(err)
	}
	if _, ready := c.SDK.Readiness(1, ""); !ready || !restored.On() {
		t.Fatal("flags not applied before Ready")
	}
}

func TestSDKFlagsReconnectAndLimit(t *testing.T) {
	c := taskCore(t)
	k := taskKit(t, c, "")
	b := k.Flag("checkout", false, "")
	if err := c.SDKFlags.Set(context.Background(), 1, "checkout", flags.Value{Kind: "bool", Bool: true}, false); err != nil {
		t.Fatal(err)
	}
	if err := k.Ready(); err != nil {
		t.Fatal(err)
	}
	// Recreate the socket without recreating the kit; Ready must be replayed
	// only after current persisted flag values have been synchronized.
	if err := c.SDK.Start(1, "", testSDKUID()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if _, ready := c.SDK.Readiness(1, ""); ready {
			if !b.On() {
				t.Fatal("ready preceded flag synchronization")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("did not reconnect")
		case <-time.After(time.Millisecond):
		}
	}
	defs := make([]flags.Definition, 100)
	for n := range defs {
		defs[n] = flags.Definition{Name: fmt.Sprintf("flag_%d", n), Kind: "bool", Default: flags.Value{Kind: "bool"}}
	}
	if _, err := c.SDKFlags.RegisterDefinitions(context.Background(), 1, "", nil, defs); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SDKFlags.RegisterDefinitions(context.Background(), 1, "canary", nil, []flags.Definition{{Name: "overflow", Kind: "bool", Default: flags.Value{Kind: "bool"}}}); err == nil {
		t.Fatal("per-bot flag limit bypassed by canary")
	}
	list, err := c.SDKFlags.List(context.Background(), 1)
	if err != nil || len(list) != 100 {
		t.Fatalf("%d %v", len(list), err)
	}
}

func TestSDKFlagResetUsesEachInstanceDefault(t *testing.T) {
	c := taskCore(t)
	main := taskKit(t, c, "")
	mainFlag := main.Flag("checkout", false, "")
	canary := taskKit(t, c, "canary")
	canaryFlag := canary.Flag("checkout", true, "")
	if mainFlag.On() || !canaryFlag.On() {
		t.Fatal("instance code defaults changed")
	}
	if err := c.SDKFlags.Set(context.Background(), 1, "checkout", flags.Value{Kind: "bool", Bool: true}, false); err != nil {
		t.Fatal(err)
	}
	if !mainFlag.On() || !canaryFlag.On() {
		t.Fatal("override was not sent to both instances")
	}
	if err := c.SDKFlags.Set(context.Background(), 1, "checkout", flags.Value{}, true); err != nil {
		t.Fatal(err)
	}
	if mainFlag.On() || !canaryFlag.On() {
		t.Fatal("reset ignored an instance's code default")
	}
}
