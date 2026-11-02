package core

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/SnimanOff/UTBPanel/backend/internal/logs"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
)

func TestOverviewReadsDiagnosticsWithoutRepairingWebhook(t *testing.T) {
	ctx := context.Background()
	c := configCore(t)
	tg := withTelegram(t, c)
	tg.hookURL = "https://another-server.invalid/hook"
	if err := c.Sec.Set(ctx, 1, secrets.TokenName, secrets.KindValue, []byte("111:good")); err != nil {
		t.Fatal(err)
	}
	if err := c.Sec.Set(ctx, 1, "KEY", secrets.KindValue, []byte("private-secret-value")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sec.Decrypt(ctx, 1, "KEY"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSetting(ctx, 1, "limits", runner.Limits{Memory: 128 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.W.Exec(`INSERT INTO webhook_state(bot_id,url) VALUES(1,'https://cached.invalid/hook')`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.W.Exec(`INSERT INTO deploys(bot_id,to_version,started_at,result) VALUES(1,'v2','2026-01-01T00:00:00Z','failed')`); err != nil {
		t.Fatal(err)
	}
	if err := c.Defer(ctx, 1, []byte(`{"private_update":"must not be in overview"}`)); err != nil {
		t.Fatal(err)
	}
	c.Logs.Add(1, "stderr", "error private-secret-value "+strings.Repeat("я", 9000), time.Now())
	c.Logs.Close()
	raw, err := os.ReadFile(filepath.Join(c.Cfg.DataDir, "bots", "1", "logs", "current.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-secret-value") {
		t.Fatal("secret persisted in bot output")
	}
	// A newly opened log store has no in-memory tail, as after a core restart.
	c.Logs = c.newLogs()
	out, err := c.Overview(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if out.Bot.ID != 1 || out.Process.Running || out.UptimeSeconds != 0 || out.Usage != nil || len(out.Resources) != 0 {
		t.Fatalf("stopped process presented as sampled: %+v", out)
	}
	if out.Deferred != 1 || out.LastDeploy == nil || out.LastDeploy.To != "v2" || out.Limits.Memory != 128<<20 || out.Limits.Threads <= 0 {
		t.Fatal(out)
	}
	if !out.WebhookLive || out.Webhook.HijackedURL != tg.hookURL || len(tg.calls) != 1 || !strings.HasPrefix(tg.calls[0], "getWebhookInfo ") {
		t.Fatal("overview repaired the webhook", out.Webhook, tg.calls)
	}
	if len(out.Errors) != 1 || len(out.Errors[0].Text) > 8200 || !utf8.ValidString(out.Errors[0].Text) {
		t.Fatal("disk error log was not bounded safely", len(out.Errors))
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-secret-value", "111:good", "private_update"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("private data leaked in overview", forbidden)
		}
	}
	if _, err := c.Overview(ctx, 999); err == nil {
		t.Fatal("missing bot accepted")
	}
	tg.mu.Lock()
	delete(tg.tokens, "111:good")
	tg.mu.Unlock()
	stale, err := c.Overview(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stale.WebhookLive || stale.WebhookError == "" || stale.Webhook.URL != "https://cached.invalid/hook" {
		t.Fatal("unavailable Telegram presented as live data", stale.Webhook, stale.WebhookError)
	}
	if lines := c.Logs.Tail(1, 20, logs.Filter{}); len(lines) != 0 {
		t.Fatal("test did not exercise the disk fallback")
	}
}

func TestResourceLimitProximityHandlesLargeCounters(t *testing.T) {
	usage := runner.Usage{RSS: 80, Threads: 79, FilesTotal: math.MaxInt64, BiggestFile: 101}
	limits := runner.Limits{Memory: 100, Threads: 100, FilesTotal: math.MaxInt64, File: 100}
	out := resourceLimits(usage, limits)
	if !out["memory"].Near || out["memory"].Reached || out["threads"].Near || !out["files_total"].Reached || !out["file"].Reached {
		t.Fatal(out)
	}
	if _, exists := out["open_files"]; exists {
		t.Fatal("unlimited resource presented as limited")
	}
}
