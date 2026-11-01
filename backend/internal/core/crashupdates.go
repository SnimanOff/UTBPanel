package core

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// CrashUpdate is an update that was with the bot when it fell.
type CrashUpdate struct {
	ID     int64           `cbor:"1,keyasint" json:"id"`
	Time   time.Time       `cbor:"2,keyasint" json:"ts"`
	Status int             `cbor:"3,keyasint" json:"status"`
	Reason string          `cbor:"4,keyasint" json:"reason"` // crash | 5xx | timeout
	Body   json.RawMessage `cbor:"5,keyasint" json:"body"`
}

// crashKeep: the last 20 per bot, 7 days.
const (
	crashKeep = 20
	crashDays = 7
)

type crashSetting struct {
	mu sync.RWMutex
	on map[int64]bool // cached crash_updates per bot
}

func (c *Core) crashOn(ctx context.Context, botID int64) bool {
	c.crashSet.mu.RLock()
	on, ok := c.crashSet.on[botID]
	c.crashSet.mu.RUnlock()
	if ok {
		return on
	}
	if st, err := c.Settings(ctx, botID); err == nil {
		json.Unmarshal(st["crash_updates"], &on)
	}
	c.crashSet.mu.Lock()
	if c.crashSet.on == nil {
		c.crashSet.on = map[int64]bool{}
	}
	c.crashSet.on[botID] = on
	c.crashSet.mu.Unlock()
	return on
}

// keepCrashUpdate saves the body of an update the bot failed on.
func (c *Core) keepCrashUpdate(o ingress.Outcome) {
	u := o.Update
	reason := ""
	switch {
	case u.Failure == "crash":
		reason = "crash"
	case o.Result == "error" && o.Status >= 500:
		reason = "5xx"
	case o.Result == "timeout":
		reason = "timeout"
	default:
		return
	}
	ctx := context.Background()
	if !c.crashOn(ctx, u.BotID) {
		return
	}
	c.DB.W.ExecContext(ctx, `INSERT INTO crash_updates (bot_id, ts, status, reason, body) VALUES (?, ?, ?, ?, ?)`,
		u.BotID, store.FormatTime(u.Received), o.Status, reason, u.Body)
	c.DB.W.ExecContext(ctx, `DELETE FROM crash_updates WHERE bot_id = ? AND (ts < ? OR id NOT IN (SELECT id FROM crash_updates WHERE bot_id = ? ORDER BY id DESC LIMIT ?))`,
		u.BotID, store.FormatTime(time.Now().Add(-crashDays*24*time.Hour)), u.BotID, crashKeep)
}

// CrashUpdates lists the kept updates of a bot, newest first.
func (c *Core) CrashUpdates(ctx context.Context, botID int64) ([]CrashUpdate, error) {
	rows, err := c.DB.R.QueryContext(ctx, `SELECT id, ts, status, reason, body FROM crash_updates WHERE bot_id = ? AND ts >= ? ORDER BY id DESC`,
		botID, store.FormatTime(time.Now().Add(-crashDays*24*time.Hour)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CrashUpdate{}
	for rows.Next() {
		var cu CrashUpdate
		var ts string
		var body []byte
		if err := rows.Scan(&cu.ID, &ts, &cu.Status, &cu.Reason, &body); err != nil {
			return nil, err
		}
		cu.Time, _ = store.ParseTime(ts)
		cu.Body = json.RawMessage(body)
		if !json.Valid(body) {
			b, _ := json.Marshal(string(body))
			cu.Body = b
		}
		out = append(out, cu)
	}
	return out, rows.Err()
}

func (c *Core) registerCrashUpdates() {
	c.Pipe.Observe = append(c.Pipe.Observe, c.keepCrashUpdate)
	c.onSettings = append(c.onSettings, func(_ context.Context, botID int64, key string) {
		if key != "crash_updates" {
			return
		}
		c.crashSet.mu.Lock()
		delete(c.crashSet.on, botID)
		c.crashSet.mu.Unlock()
		if on := c.crashOn(context.Background(), botID); !on {
			// Turning it off removes what was kept.
			c.DB.W.Exec(`DELETE FROM crash_updates WHERE bot_id = ?`, botID)
		}
	})
	c.Mux.Handle("crash_updates.list", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) ([]CrashUpdate, error) {
		if err := c.botExists(ctx, id); err != nil {
			return nil, err
		}
		return c.CrashUpdates(ctx, id)
	}))
}
