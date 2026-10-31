package core

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/edge"
	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/updrules"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// WaitForBot: a usual restart takes 1–2 s, so users notice nothing.
var WaitForBot = 5 * time.Second

// delivery orders a bot's updates: queued ones go first, new ones wait.
type delivery struct {
	mu   sync.Mutex
	bots map[int64]*sync.RWMutex
}

func (d *delivery) lock(id int64) *sync.RWMutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.bots == nil {
		d.bots = map[int64]*sync.RWMutex{}
	}
	l := d.bots[id]
	if l == nil {
		l = &sync.RWMutex{}
		d.bots[id] = l
	}
	return l
}

// Defer stores an update for later delivery.
func (c *Core) Defer(ctx context.Context, botID int64, body []byte) error {
	_, err := c.DB.W.ExecContext(ctx, `INSERT INTO deferred (bot_id, body, created_at) VALUES (?, ?, ?)`, botID, body, store.Now())
	return err
}

// DeferredCount is shown on the bot page.
func (c *Core) DeferredCount(ctx context.Context, botID int64) int {
	var n int
	c.DB.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM deferred WHERE bot_id = ?`, botID).Scan(&n)
	return n
}

func (c *Core) unavailableText(ctx context.Context, botID int64) string {
	st, err := c.Settings(ctx, botID)
	if err == nil {
		var texts struct {
			Unavailable string `json:"unavailable"`
		}
		json.Unmarshal(st["texts"], &texts)
		if texts.Unavailable != "" {
			return texts.Unavailable
		}
	}
	return DefaultUnavailable
}

// botUnavailable waits for a crashed or restarting bot up to WaitForBot;
// then answers by the rules with "bot unavailable". A bot stopped by the
// owner has no webhook, so its updates never get here.
func (c *Core) botUnavailable(ctx context.Context, u *ingress.Update) (edge.Reply, bool) {
	deadline := time.Now().Add(WaitForBot)
	for time.Now().Before(deadline) {
		if c.Runner.IsRunning(u.BotID) {
			return edge.Reply{}, false
		}
		select {
		case <-ctx.Done():
			deadline = time.Now()
		case <-time.After(100 * time.Millisecond):
		}
	}
	d := updrules.Decide(u.Info, updrules.Unavailable, c.unavailableText(ctx, u.BotID), c.Pipe.Replies)
	switch d.Action {
	case updrules.Reply:
		return edge.Reply{Status: http.StatusOK, ContentType: "application/json", Body: d.Body}, true
	case updrules.Defer:
		if err := c.Defer(ctx, u.BotID, u.Body); err != nil {
			return edge.Reply{Status: http.StatusInternalServerError}, true
		}
	}
	return edge.Reply{Status: http.StatusOK}, true
}

// FlushDeferred delivers queued updates to a ready bot, oldest first. New
// updates of this bot wait meanwhile. A method in the bot's answer is
// called through the Bot API, since there is no webhook response to put it in.
func (c *Core) FlushDeferred(ctx context.Context, botID int64) {
	l := c.delivery.lock(botID)
	l.Lock()
	defer l.Unlock()
	for {
		if !c.Runner.IsRunning(botID) || c.Paused(ctx, botID) {
			return
		}
		var id int64
		var body []byte
		err := c.DB.R.QueryRowContext(ctx, `SELECT id, body FROM deferred WHERE bot_id = ? ORDER BY id LIMIT 1`, botID).Scan(&id, &body)
		if err != nil {
			return // empty
		}
		rep := c.Pipe.Deliver(ctx, botID, body)
		if rep.Status >= 500 || rep.Status == 0 {
			c.DB.W.ExecContext(ctx, `UPDATE deferred SET attempts = attempts + 1, last_error = ? WHERE id = ?`, http.StatusText(rep.Status), id)
			return // try again on the next start or flush
		}
		c.callReplyMethod(ctx, botID, rep.Body)
		c.DB.W.ExecContext(ctx, `DELETE FROM deferred WHERE id = ?`, id)
		c.Logs.Add(botID, "core", "delivered a deferred update", nowUTC())
	}
}

func (c *Core) callReplyMethod(ctx context.Context, botID int64, body []byte) {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return
	}
	method, _ := m["method"].(string)
	if method == "" {
		return
	}
	delete(m, "method")
	tok, err := c.Token(ctx, botID)
	if err != nil {
		return
	}
	if err := c.TG.Call(ctx, tok, method, m, nil); err != nil {
		c.Log.Warn("method from a deferred update failed", "bot", botID, "method", method, "err", err)
	}
}

// Paused reports maintenance or pause; filled in with maintenance.
func (c *Core) Paused(ctx context.Context, botID int64) bool {
	for _, fn := range c.pausedChecks {
		if fn(ctx, botID) {
			return true
		}
	}
	return false
}

func (c *Core) registerDeferred() {
	c.Pipe.Defer = func(ctx context.Context, u *ingress.Update) error { return c.Defer(ctx, u.BotID, u.Body) }
	c.Pipe.Unavailable = c.botUnavailable
	c.Pipe.Gate = func(botID int64) func() {
		l := c.delivery.lock(botID)
		l.RLock()
		return l.RUnlock
	}
	// The edge hands over what it queued while the core was down.
	c.Mux.Handle(edge.MethodDeferred, wire.Func(func(ctx context.Context, _ *wire.Conn, d edge.Deferred) (struct{}, error) {
		for _, u := range d.Updates {
			if err := c.Defer(ctx, d.BotID, u); err != nil {
				return struct{}{}, err
			}
		}
		c.Log.Info("took deferred updates from the edge", "bot", d.BotID, "count", len(d.Updates))
		go c.FlushDeferred(context.Background(), d.BotID)
		return struct{}{}, nil
	}))
	c.Mux.Handle("ingress.deferred_count", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (int, error) {
		return c.DeferredCount(ctx, id), nil
	}))
}
