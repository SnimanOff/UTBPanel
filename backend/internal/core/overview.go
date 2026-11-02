package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/logs"
	"github.com/SnimanOff/UTBPanel/backend/internal/maintenance"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

type ResourceLimit struct {
	Used    int64   `json:"used"`
	Limit   int64   `json:"limit"`
	Ratio   float64 `json:"ratio"`
	Near    bool    `json:"near"`
	Reached bool    `json:"reached"`
}

// Overview is a bounded diagnostic snapshot. It reads Telegram's webhook
// information without repairing it, and falls back to the cached state.
type Overview struct {
	At            time.Time                `json:"at"`
	Bot           Bot                      `json:"bot"`
	Process       runner.Status            `json:"process"`
	UptimeSeconds int64                    `json:"uptime_seconds"`
	Errors        []logs.Line              `json:"errors"`
	Webhook       WebhookState             `json:"webhook"`
	WebhookLive   bool                     `json:"webhook_live"`
	WebhookError  string                   `json:"webhook_error,omitempty"`
	LastDeploy    *DeployRecord            `json:"last_deploy"`
	Limits        runner.Limits            `json:"limits"`
	Usage         *runner.Usage            `json:"usage"`
	Resources     map[string]ResourceLimit `json:"resources"`
	Deferred      int                      `json:"deferred"`
	Canary        *Canary                  `json:"canary"`
	Maintenance   *maintenance.M           `json:"maintenance"`
}

func (c *Core) Overview(ctx context.Context, id int64) (Overview, error) {
	out := Overview{At: time.Now().UTC(), Errors: []logs.Line{}, Resources: map[string]ResourceLimit{}}
	var err error
	out.Bot, err = c.Bot(ctx, id)
	if err != nil {
		return out, err
	}
	out.Process = c.Runner.Status(id)
	if out.Process.Running && !out.Process.Started.IsZero() {
		out.UptimeSeconds = max(0, int64(out.At.Sub(out.Process.Started).Seconds()))
	}
	out.Errors = c.Logs.Tail(id, 20, logs.Filter{MinLevel: logs.LevelError})
	if len(out.Errors) == 0 {
		out.Errors, err = c.Logs.Search(id, logs.Filter{MinLevel: logs.LevelError}, time.Time{}, time.Time{}, 20)
		if err != nil {
			return out, err
		}
	}
	if out.Errors == nil {
		out.Errors = []logs.Line{}
	}
	for index := range out.Errors {
		line := &out.Errors[index]
		line.Text = c.Sec.Redact.Redact(line.Text)
		if len(line.Text) > 8192 {
			line.Text = strings.ToValidUTF8(line.Text[:8192], "") + "…"
		}
	}
	var setAt, checkedAt string
	err = c.DB.R.QueryRowContext(ctx, `SELECT url,COALESCE(set_at,''),COALESCE(checked_at,''),last_error,hijacked_url FROM webhook_state WHERE bot_id=?`, id).Scan(&out.Webhook.URL, &setAt, &checkedAt, &out.Webhook.LastError, &out.Webhook.HijackedURL)
	if err != nil && err != sql.ErrNoRows {
		return out, err
	}
	out.Webhook.SetAt, _ = store.ParseTime(setAt)
	out.Webhook.CheckedAt, _ = store.ParseTime(checkedAt)
	if token, err := c.Token(ctx, id); err == nil {
		read, cancel := context.WithTimeout(ctx, 2*time.Second)
		info, err := c.TG.GetWebhookInfo(read, token)
		cancel()
		if err == nil {
			out.WebhookLive = true
			out.Webhook.URL = info.URL
			out.Webhook.Pending = info.PendingUpdateCount
			out.Webhook.LastError = info.LastErrorMessage
			out.Webhook.CheckedAt = time.Now().UTC()
			out.Webhook.HijackedURL = ""
			if info.URL != "" && info.URL != c.WebhookURL(id) {
				out.Webhook.HijackedURL = info.URL
			}
		} else {
			out.WebhookError = err.Error()
		}
	} else {
		out.WebhookError = err.Error()
	}
	deploys, err := c.Deploys(ctx, id, 1)
	if err != nil {
		return out, err
	}
	if len(deploys) > 0 {
		out.LastDeploy = &deploys[0]
	}
	settings, err := c.Settings(ctx, id)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(settings["limits"], &out.Limits); len(settings["limits"]) > 0 && err != nil {
		return out, err
	}
	d := c.Cfg.Limits
	out.Limits = out.Limits.Or(runner.Limits{Memory: int64(d.Memory), File: int64(d.File), FilesTotal: int64(d.FilesTotal), OpenFiles: int64(d.OpenFiles), Threads: int64(d.Threads)})
	if current, ok := c.Runner.CurrentLimits(id); ok {
		out.Limits = current
	}
	if usage, ok := c.Runner.Usage(id); ok && out.Process.Running {
		out.Usage = &usage
		out.Resources = resourceLimits(usage, out.Limits)
	}
	if err := c.DB.R.QueryRowContext(ctx, `SELECT count(*) FROM deferred WHERE bot_id=?`, id).Scan(&out.Deferred); err != nil {
		return out, err
	}
	out.Canary, err = c.CanaryOf(ctx, id)
	if err != nil {
		return out, err
	}
	if m, ok := c.Maint.Get(id); ok {
		out.Maintenance = &m
	}
	return out, nil
}

func resourceLimits(usage runner.Usage, limits runner.Limits) map[string]ResourceLimit {
	out := map[string]ResourceLimit{}
	for name, pair := range map[string][2]int64{"memory": {usage.RSS, limits.Memory}, "open_files": {usage.OpenFiles, limits.OpenFiles}, "threads": {usage.Threads, limits.Threads}, "files_total": {usage.FilesTotal, limits.FilesTotal}, "file": {usage.BiggestFile, limits.File}} {
		if pair[1] <= 0 {
			continue
		}
		ratio := float64(pair[0]) / float64(pair[1])
		out[name] = ResourceLimit{Used: pair[0], Limit: pair[1], Ratio: ratio, Near: ratio >= 0.8, Reached: pair[0] >= pair[1]}
	}
	return out
}
func (c *Core) registerOverview() {
	c.Mux.Handle("bots.overview", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (Overview, error) { return c.Overview(ctx, id) }))
}
