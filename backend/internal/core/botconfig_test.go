package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/metrics"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
	"github.com/SnimanOff/UTBPanel/backend/sdk/flags"
)

func configCore(t *testing.T) *Core {
	t.Helper()
	c := newCore(t)
	_, err := c.DB.W.Exec(`INSERT INTO bots(id,name,port,created_at,settings) VALUES(1,'a',20001,'x','{"profile_cache":{},"env":{"OLD":"1"},"private":{"keep":true}}')`)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func cloneConfig(d botconfig.Document) botconfig.Document {
	raw, _ := json.Marshal(d)
	var out botconfig.Document
	json.Unmarshal(raw, &out)
	return out
}
func conflict(err error) bool { var e *wire.Error; return errors.As(err, &e) && e.Code == "conflict" }

func TestConfigSecretIsolationAndReplacement(t *testing.T) {
	ctx := context.Background()
	c := configCore(t)
	for name, value := range map[string]string{secrets.TokenName: "111:top-secret", secrets.WebhookSecretName: "webhook-secret", "PAYMENT_KEY": "payment-secret"} {
		if err := c.Sec.Set(ctx, 1, name, secrets.KindValue, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := c.ConfigSnapshot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := botconfig.YAML(snap.Document)
	if strings.Contains(raw, "top-secret") || strings.Contains(raw, "payment-secret") || strings.Contains(raw, "BOT_TOKEN") || len(snap.Document.Secrets) != 1 {
		t.Fatal(raw)
	}
	d := cloneConfig(snap.Document)
	d.Env = map[string]string{"NEW": "2"}
	d.Secrets = []string{"MISSING"}
	d.TZ = "UTC"
	d.CrashUpdates = true
	out, err := c.ApplyConfig(ctx, botconfig.ApplyRequest{BotID: 1, Version: snap.Version, Document: d})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Applied || !out.Restart || len(out.MissingSecrets) != 1 || out.MissingSecrets[0] != "MISSING" {
		t.Fatalf("%+v", out)
	}
	stored, _ := c.Settings(ctx, 1)
	if !strings.Contains(string(stored["private"]), "true") || strings.Contains(string(stored["env"]), "OLD") || !c.crashOn(ctx, 1) {
		t.Fatalf("settings not replaced: %s", stored)
	}
	if _, err := c.Sec.Decrypt(ctx, 1, "PAYMENT_KEY"); err != secrets.ErrNotFound {
		t.Fatalf("removed secret: %v", err)
	}
	if _, err := c.Token(ctx, 1); err != nil {
		t.Fatal("reserved token was removed", err)
	}
	if _, err := c.ApplyConfig(ctx, botconfig.ApplyRequest{BotID: 1, Version: snap.Version, Document: d}); !conflict(err) {
		t.Fatalf("stale write: %v", err)
	}
	if _, err := c.ApplyConfig(ctx, botconfig.ApplyRequest{BotID: 1, Version: out.Snapshot.Version, Document: out.Snapshot.Document}); err != nil {
		t.Fatal("idempotent apply", err)
	}
}

func TestConfigRejectsSecretAndConcurrentChanges(t *testing.T) {
	ctx := context.Background()
	c := configCore(t)
	if err := c.Sec.Set(ctx, 1, "KEY", secrets.KindValue, []byte("same")); err != nil {
		t.Fatal(err)
	}
	snap, _ := c.ConfigSnapshot(ctx, 1)
	if err := c.Sec.Set(ctx, 1, "KEY", secrets.KindValue, []byte("same")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ApplyConfig(ctx, botconfig.ApplyRequest{BotID: 1, Version: snap.Version, Document: snap.Document}); !conflict(err) {
		t.Fatalf("secret update did not conflict: %v", err)
	}
	snap, _ = c.ConfigSnapshot(ctx, 1)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, zone := range []string{"UTC", "Europe/Berlin"} {
		d := cloneConfig(snap.Document)
		d.TZ = zone
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.ApplyConfig(ctx, botconfig.ApplyRequest{BotID: 1, Version: snap.Version, Document: d})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	ok, conflicts := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if conflict(err) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", ok, conflicts)
	}
}

func TestConfigValidationBeforeMutation(t *testing.T) {
	c := configCore(t)
	ctx := context.Background()
	snap, _ := c.ConfigSnapshot(ctx, 1)
	d := cloneConfig(snap.Document)
	d.TZ = "UTC"
	d.Flags["unknown"] = botconfig.FlagValue{Kind: "bool", Bool: true}
	if _, err := c.ApplyConfig(ctx, botconfig.ApplyRequest{BotID: 1, Version: snap.Version, Document: d}); err == nil {
		t.Fatal("unregistered flag accepted")
	}
	after, _ := c.ConfigSnapshot(ctx, 1)
	if after.Version != snap.Version {
		t.Fatal("invalid document partially applied")
	}
}

func TestConfigCanRemoveUnregisteredImportedFlag(t *testing.T) {
	c := configCore(t)
	ctx := context.Background()
	if err := c.SetSetting(ctx, 1, "pending_flags", map[string]bool{"removed_from_code": true}); err != nil {
		t.Fatal(err)
	}
	snap, err := c.ConfigSnapshot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	delete(snap.Document.Flags, "removed_from_code")
	out, err := c.ApplyConfig(ctx, botconfig.ApplyRequest{BotID: 1, Version: snap.Version, Document: snap.Document})
	if err != nil || len(out.Snapshot.Document.Flags) != 0 {
		t.Fatal(out, err)
	}
}

func TestConfigProfilePartialFailure(t *testing.T) {
	ctx := context.Background()
	c := configCore(t)
	if err := c.Sec.Set(ctx, 1, secrets.TokenName, secrets.KindValue, []byte("111:private")); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/setMyName") {
			w.WriteHeader(429)
			io.WriteString(w, `{"ok":false,"error_code":429,"description":"wait"}`)
		} else {
			io.WriteString(w, `{"ok":true,"result":true}`)
		}
	}))
	defer srv.Close()
	c.TG.Base = srv.URL
	snap, err := c.ConfigSnapshot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	d := cloneConfig(snap.Document)
	d.Profile.Name["default"] = "New"
	d.Profile.Description["default"] = "Accepted"
	d.Env = map[string]string{"NEW": "2"}
	out, err := c.ApplyConfig(ctx, botconfig.ApplyRequest{BotID: 1, Version: snap.Version, Document: d})
	if err != nil {
		t.Fatal(err)
	}
	if out.Applied || len(out.ProfileErrors) != 1 || out.ProfileErrors["default.name"] != "wait" {
		t.Fatalf("%+v", out)
	}
	if out.Snapshot.Version == snap.Version || out.Snapshot.Document.Profile.Description["default"] != "Accepted" || out.Snapshot.Document.Profile.Name["default"] != "" || out.Snapshot.Document.Env["OLD"] != "1" {
		t.Fatalf("partial result was not tracked: %+v", out.Snapshot)
	}
}

func TestConfigTypedFlagsAndReset(t *testing.T) {
	ctx := context.Background()
	c := configCore(t)
	_, err := c.SDKFlags.RegisterDefinitions(ctx, 1, "main", nil, []flags.Definition{{Name: "wait", Kind: "duration", Default: flags.Value{Kind: "duration", Int: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := c.ConfigSnapshot(ctx, 1)
	d := cloneConfig(snap.Document)
	d.Flags["wait"] = botconfig.FlagValue{Kind: "string", Text: "3s"}
	caller := &configCaller{c: c}
	var out botconfig.ApplyResult
	if err := caller.Call(ctx, "config.apply", botconfig.ApplyRequest{BotID: 1, Version: snap.Version, Document: d}, &out); err != nil {
		t.Fatal(err)
	}
	if value := out.Snapshot.Document.Flags["wait"]; value.Kind != "duration" || value.Int != 3_000_000_000 {
		t.Fatalf("typed CBOR flag: %+v", value)
	}
	d = cloneConfig(out.Snapshot.Document)
	delete(d.Flags, "wait")
	out, err = c.ApplyConfig(ctx, botconfig.ApplyRequest{BotID: 1, Version: out.Snapshot.Version, Document: d})
	if err != nil || len(out.Snapshot.Document.Flags) != 0 {
		t.Fatalf("reset: %+v %v", out, err)
	}
	list, _ := c.SDKFlags.List(ctx, 1)
	if list[0].Overridden || list[0].Value.Int != 1 {
		t.Fatalf("default not restored: %+v", list)
	}
}

// Exercise the CBOR representation used between panel and core, rather than
// copying structs directly (flag values and optional fields must survive it).
type configCaller struct {
	c           *Core
	beforeApply func()
}

func (cc *configCaller) Call(ctx context.Context, method string, in, out any) error {
	var value any
	var err error
	switch method {
	case "config.get":
		value, err = cc.c.ConfigSnapshot(ctx, in.(int64))
	case "config.apply":
		if cc.beforeApply != nil {
			cc.beforeApply()
		}
		raw, e := wire.Marshal(in)
		if e != nil {
			return e
		}
		var req botconfig.ApplyRequest
		if e := wire.Unmarshal(raw, &req); e != nil {
			return e
		}
		value, err = cc.c.ApplyConfig(ctx, req)
	default:
		return errors.New("unexpected method")
	}
	if err != nil {
		return err
	}
	raw, err := wire.Marshal(value)
	if err != nil {
		return err
	}
	return wire.Unmarshal(raw, out)
}

func TestConfigServiceAlertsAndSecondVersionCheck(t *testing.T) {
	ctx := context.Background()
	c := configCore(t)
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rules := &alerts.Rules{S: &metrics.Storage{DB: db}}
	caller := &configCaller{c: c}
	svc := &botconfig.Service{Core: caller, Rules: rules}
	rules.Lock = svc.Guard.Lock
	if _, err := rules.Save(ctx, alerts.Rule{BotID: 0, Metric: "cpu", Op: ">", Value: 90, Minutes: 5}); err != nil {
		t.Fatal(err)
	}
	view, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	d := cloneConfig(view.Document)
	d.TZ = "UTC"
	d.Alerts = []botconfig.Alert{{Metric: "p95", Op: ">", Value: 2000, Minutes: 5, Enabled: true}}
	edit := botconfig.Edit{Version: view.Document.Version, Document: &d}
	diff, err := svc.Diff(ctx, 1, edit)
	if err != nil || diff.Confirm || len(diff.Changes) != 2 {
		t.Fatalf("diff: %+v %v", diff, err)
	}
	result, err := svc.Apply(ctx, 1, edit, ctx)
	if err != nil || !result.Applied {
		t.Fatalf("apply: %+v %v", result, err)
	}
	all, _ := rules.List(ctx, -1)
	if len(all) != 2 {
		t.Fatal("common rule lost", all)
	}
	old := cloneConfig(result.Document)
	if _, err := rules.Save(ctx, alerts.Rule{BotID: 1, Metric: "cpu", Op: ">", Value: 80, Minutes: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Apply(ctx, 1, botconfig.Edit{Version: old.Version, Document: &old}, ctx); !conflict(err) {
		t.Fatal("alert edit ignored", err)
	}
	view, _ = svc.Get(ctx, 1)
	d = cloneConfig(view.Document)
	d.TZ = "Europe/Berlin"
	caller.beforeApply = func() {
		if err := c.SetSetting(context.Background(), 1, "tz", "Asia/Tokyo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Apply(ctx, 1, botconfig.Edit{Version: view.Document.Version, Document: &d}, ctx); !conflict(err) {
		t.Fatal("core must recheck after panel comparison", err)
	}
}
