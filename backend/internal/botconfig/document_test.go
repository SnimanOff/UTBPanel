package botconfig

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk/flags"
)

func TestDocumentYAMLAndJSON(t *testing.T) {
	d := Document{TZ: "UTC", Secrets: []string{"PAYMENT_KEY"}, Limits: Limits{Memory: 256 << 20}, Flags: map[string]FlagValue{"huge": FlagValue(flags.Value{Kind: "int", Int: math.MaxInt64}), "wait": FlagValue(flags.Value{Kind: "duration", Int: int64(time.Second)})}}
	Normalize(&d)
	d.Antiflood.WarnText = "Слишком часто"
	raw, err := YAML(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "memory: 256M") || !strings.Contains(raw, "warn_text:") {
		t.Fatal(raw)
	}
	decoded, err := Decode([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Flags["huge"].Int != math.MaxInt64 || decoded.Flags["wait"].Text != "1s" || decoded.Limits.Memory != 256<<20 || decoded.Antiflood.WarnText != d.Antiflood.WarnText {
		t.Fatalf("round trip: %+v", decoded)
	}
	// The duration's wire type is restored using the registered definition.
	decoded.Flags["wait"] = d.Flags["wait"]
	if !reflect.DeepEqual(d, decoded) {
		t.Fatalf("YAML lost settings: %+v", decoded)
	}
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var fromJSON Document
	if err := json.Unmarshal(encoded, &fromJSON); err != nil {
		t.Fatal(err)
	}
	if fromJSON.Flags["huge"].Int != math.MaxInt64 {
		t.Fatal("integer precision lost")
	}
}

func TestRejectAmbiguousOrSecretDocuments(t *testing.T) {
	for _, raw := range []string{"secrets: {PAYMENT_KEY: value}", "BOT_TOKEN: secret", "secrets: [BOT_TOKEN]", "secrets: [A, A]", "tz: UTC\n---\ntz: UTC", "tz: UTC\ntz: GMT", "profile: {menu: {type: unknown}}", "limits: {memory: -1}", "flags: {bad: [true]}"} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	d := Document{}
	Normalize(&d)
	valid, _ := YAML(d)
	for _, raw := range []string{strings.Replace(valid, "secrets: []", "secrets: [BOT_TOKEN]", 1), strings.Replace(valid, "memory: \"0\"", "memory: 9223372036854775807G", 1), valid + "BOT_TOKEN: secret\n"} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Errorf("accepted invalid complete document: %s", raw)
		}
	}
}

func TestDiffLevelsAndExactNumbers(t *testing.T) {
	a := Document{Secrets: []string{"OLD"}, Flags: map[string]FlagValue{"n": {Kind: "int", Int: 9007199254740992}}}
	Normalize(&a)
	raw, _ := json.Marshal(a)
	var b Document
	json.Unmarshal(raw, &b)
	b.Secrets = []string{"NEW"}
	b.Env["LOG_LEVEL"] = "debug"
	b.Antiflood.Enabled = true
	b.Antiflood.Limit = 30
	b.Closed.Text = "Closed"
	b.Flags["n"] = FlagValue{Kind: "int", Int: 9007199254740993}
	diff := Diff(a, b)
	if !diff.Confirm || !diff.Restart {
		t.Fatalf("%+v", diff)
	}
	levels := map[string]string{}
	for _, c := range diff.Changes {
		levels[c.Path] = c.Level
	}
	for path, want := range map[string]string{"secrets": "confirm", "env.LOG_LEVEL": "confirm", "antiflood.enabled": "confirm", "antiflood.limit": "write", "closed.text": "write", "flags.n": "write"} {
		if levels[path] != want {
			t.Errorf("%s: %q", path, levels[path])
		}
	}
}

func TestGuardCancellationAndReentrancy(t *testing.T) {
	var g Guard
	ctx, unlock, err := g.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	_, nested, err := g.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nested()
	timed, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := g.Lock(timed); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
}

func TestSchemaDerivedFromOpenAPI(t *testing.T) {
	schema, err := Schema()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "#/components/") || !strings.Contains(string(raw), "#/$defs/MenuButton") || !strings.Contains(string(raw), "secret values") {
		t.Fatal(string(raw))
	}
}
