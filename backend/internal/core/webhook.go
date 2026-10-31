package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// WebhookCheckEvery: a copy of the bot started elsewhere with the same
// token silently takes the webhook; this check notices it.
var WebhookCheckEvery = 5 * time.Minute

// WebhookState is shown on the bot page.
type WebhookState struct {
	URL         string    `cbor:"1,keyasint" json:"url"`
	SetAt       time.Time `cbor:"2,keyasint" json:"set_at,omitzero"`
	CheckedAt   time.Time `cbor:"3,keyasint" json:"checked_at,omitzero"`
	LastError   string    `cbor:"4,keyasint" json:"last_error,omitempty"`
	HijackedURL string    `cbor:"5,keyasint" json:"hijacked_url,omitempty"`
	Pending     int       `cbor:"6,keyasint" json:"pending"`
}

func (c *Core) dropPending(ctx context.Context, id int64) bool {
	st, err := c.Settings(ctx, id)
	if err != nil {
		return false
	}
	var stop struct {
		DropPending bool `json:"drop_pending"`
	}
	json.Unmarshal(st["stop"], &stop)
	return stop.DropPending
}

// SetWebhook points Telegram to the edge. Only the core manages webhooks:
// the bot's own setWebhook calls are answered by the proxy.
func (c *Core) SetWebhook(ctx context.Context, id int64) error {
	tok, err := c.Token(ctx, id)
	if err != nil {
		return err
	}
	t, err := c.target(ctx, id)
	if err != nil {
		return err
	}
	p := telegram.SetWebhook{
		URL: c.WebhookURL(id), SecretToken: t.Secret, MaxConnections: 40,
		DropPendingUpdates: c.dropPending(ctx, id),
	}
	if pem := c.EdgeCert(ctx); len(pem) > 0 {
		p.Certificate = telegram.InputFile{Name: "cert.pem", Data: pem}
	}
	if err := c.TG.SetWebhook(ctx, tok, p); err != nil {
		c.Logs.Add(id, "core", "setWebhook failed: "+err.Error(), nowUTC())
		c.Ev.Emit(events.WebhookError, id, events.Error, map[string]any{"error": err.Error()})
		return err
	}
	c.DB.W.ExecContext(ctx, `INSERT INTO webhook_state (bot_id, url, set_at, fixed_count, hijacked_url) VALUES (?, ?, ?, 0, '')
		ON CONFLICT (bot_id) DO UPDATE SET url = excluded.url, set_at = excluded.set_at, fixed_count = 0, hijacked_url = ''`,
		id, p.URL, store.Now())
	c.Logs.Add(id, "core", "webhook set: "+p.URL, nowUTC())
	return nil
}

// DeleteWebhook removes the webhook when the owner stops the bot; updates
// wait at Telegram (up to 24 h) unless dropping is on.
func (c *Core) DeleteWebhook(ctx context.Context, id int64) {
	tok, err := c.Token(ctx, id)
	if err != nil {
		return
	}
	if err := c.TG.DeleteWebhook(ctx, tok, c.dropPending(ctx, id)); err != nil {
		c.Logs.Add(id, "core", "deleteWebhook failed: "+err.Error(), nowUTC())
		return
	}
	c.DB.W.ExecContext(ctx, `UPDATE webhook_state SET url = '' WHERE bot_id = ?`, id)
	c.Logs.Add(id, "core", "webhook removed", nowUTC())
}

// CheckWebhook compares getWebhookInfo with our address. A foreign address
// is fixed once; if it happens again only an event is sent, so the core
// and a misbehaving copy do not fight forever.
func (c *Core) CheckWebhook(ctx context.Context, id int64) (WebhookState, error) {
	var st WebhookState
	tok, err := c.Token(ctx, id)
	if err != nil {
		return st, err
	}
	info, err := c.TG.GetWebhookInfo(ctx, tok)
	if err != nil {
		return st, err
	}
	want := c.WebhookURL(id)
	st = WebhookState{URL: info.URL, CheckedAt: time.Now().UTC(), LastError: info.LastErrorMessage, Pending: info.PendingUpdateCount}
	var fixed int
	c.DB.R.QueryRowContext(ctx, `SELECT fixed_count FROM webhook_state WHERE bot_id = ?`, id).Scan(&fixed)
	if info.URL != want {
		st.HijackedURL = info.URL
		c.Ev.Emit(events.WebhookHijacked, id, events.Error, map[string]any{"url": info.URL, "fixed": fixed == 0})
		if fixed == 0 {
			if err := c.SetWebhook(ctx, id); err == nil {
				fixed = 1
				c.DB.W.ExecContext(ctx, `UPDATE webhook_state SET fixed_count = 1, hijacked_url = ? WHERE bot_id = ?`, info.URL, id)
			}
		} else {
			c.DB.W.ExecContext(ctx, `UPDATE webhook_state SET hijacked_url = ? WHERE bot_id = ?`, info.URL, id)
		}
	}
	c.DB.W.ExecContext(ctx, `UPDATE webhook_state SET checked_at = ?, last_error = ? WHERE bot_id = ?`, store.Now(), info.LastErrorMessage, id)
	return st, nil
}

func (c *Core) watchWebhooks(ctx context.Context) {
	t := time.NewTicker(WebhookCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, id := range c.runningBots() {
			if _, err := c.CheckWebhook(ctx, id); err != nil && ctx.Err() == nil {
				c.Log.Warn("webhook check failed", "bot", id, "err", err)
			}
		}
	}
}

func (c *Core) runningBots() []int64 {
	bs, _ := c.Bots(context.Background())
	var ids []int64
	for _, b := range bs {
		if c.Runner.IsRunning(b.ID) {
			ids = append(ids, b.ID)
		}
	}
	return ids
}

func (c *Core) registerWebhooks() {
	c.onStarted = append(c.onStarted, func(ctx context.Context, b runner.Bot) {
		c.SetWebhook(ctx, b.ID)
	})
	// Only an owner's stop removes the webhook; a core shutdown leaves it,
	// and the edge answers while the core is away.
	c.onStopping = append(c.onStopping, func(ctx context.Context, b runner.Bot) {
		c.DeleteWebhook(ctx, b.ID)
	})
	c.Mux.Handle("webhook.info", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (WebhookState, error) {
		return c.CheckWebhook(ctx, id)
	}))
}
