package flags

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type client struct{}

func (client) Connected() bool                              { return false }
func (client) Use(string) error                             { return nil }
func (client) Call(context.Context, string, any, any) error { return nil }

func TestAutonomousEnvAndVersions(t *testing.T) {
	t.Setenv("TGPANEL_FLAG_CHECKOUT", "true")
	t.Setenv("TGPANEL_FLAG_RETRIES", "4")
	t.Setenv("TGPANEL_FLAG_LABEL", "env")
	t.Setenv("TGPANEL_FLAG_TIMEOUT", "2m")
	r := New(client{}, nil)
	b := r.Bool("checkout", false, "checkout")
	i := r.Int("retries", 1, "")
	s := r.String("label", "default", "")
	d := r.Duration("timeout", time.Second, "")
	if !b.On() || i.Value() != 4 || s.Value() != "env" || d.Value() != 2*time.Minute {
		t.Fatal("env fallback failed")
	}
	r.Update(map[string]State{"checkout": {Value: Value{Kind: "bool", Bool: false}, Version: 2}})
	r.Update(map[string]State{"checkout": {Value: Value{Kind: "bool", Bool: true}, Version: 1}})
	if b.On() {
		t.Fatal("stale update replaced a newer value")
	}
	r.Update(map[string]State{"checkout": {Version: 3, Reset: true}})
	if b.On() {
		t.Fatal("core reset must use code default, not autonomous env")
	}
	r.Disconnected()
	if !b.On() {
		t.Fatal("disconnection did not restore autonomous env")
	}
}

func TestFlagLimitAndInvalidEnv(t *testing.T) {
	t.Setenv("TGPANEL_FLAG_RETRIES", "bad")
	r := New(client{}, nil)
	if v := r.Int("retries", 2, ""); v.Value() != 2 {
		t.Fatal(v.Value())
	}
	for n := 0; n < MaxFlags; n++ {
		r.Bool(fmt.Sprintf("flag_%d", n), true, "")
	}
	if len(r.entries) != MaxFlags {
		t.Fatal(len(r.entries))
	}
	if v := r.Bool("extra", true, ""); !v.On() {
		t.Fatal("ignored flag lost its default")
	}
}
