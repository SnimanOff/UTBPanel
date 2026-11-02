package botconfig

import (
	"strings"
	"testing"
)

func TestReleaseFileOverlay(t *testing.T) {
	base := Document{Env: map[string]string{"A": "1"}, TZ: "Europe/Moscow", Secrets: []string{"KEY"}}
	Normalize(&base)
	f, err := ParseRelease([]byte("version: 3\ntz: Europe/Berlin\nenv: { B: '2' }\n"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.Over(base)
	if err != nil {
		t.Fatal(err)
	}
	if d.TZ != "Europe/Berlin" || d.Env["B"] != "2" || d.Env["A"] != "" || len(d.Secrets) != 1 {
		t.Fatalf("%+v", d)
	}
	if base.Env["A"] != "1" {
		t.Fatal("base changed")
	}
	diff := Diff(base, d)
	if !diff.Confirm || !diff.Restart {
		t.Fatalf("env needs confirmation and a restart: %+v", diff)
	}
	if f, _ := ParseRelease(nil); f.Env != nil {
		t.Fatal("an empty file changes nothing")
	}
	for _, bad := range []string{"name: echo\n", "group: { id: 1, name: x }\n", "a: 1\n---\nb: 2\n"} {
		if _, err := ParseRelease([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	f, _ = ParseRelease([]byte("tz: Mars/Olympus\n"))
	if _, err := f.Over(base); err == nil || !strings.Contains(err.Error(), "time zone") {
		t.Fatalf("invalid values must be refused: %v", err)
	}
}
