package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// CanaryOffset separates canary ports from bot ports (20000+ → 30000+);
// the canary's proxy port is CanaryPort + ProxyOffset.
const CanaryOffset = 10000

// CanaryPort is the port of a bot's canary.
func CanaryPort(botPort int) int { return botPort + CanaryOffset }

// CanaryReq starts or changes a canary.
type CanaryReq struct {
	BotID     int64         `cbor:"1,keyasint"`
	Version   string        `cbor:"2,keyasint,omitempty"`
	Share     int           `cbor:"3,keyasint"`
	Users     []int64       `cbor:"4,keyasint"`
	AutoAfter int           `cbor:"5,keyasint,omitempty"` // minutes, 0 = off
	Reason    string        `cbor:"6,keyasint,omitempty"` // stop
	Actor     journal.Actor `cbor:"9,keyasint"`
}

// Canary is the state shown in the panel.
type Canary struct {
	BotID     int64     `cbor:"1,keyasint" json:"bot_id"`
	Version   string    `cbor:"2,keyasint" json:"version"`
	Share     int       `cbor:"3,keyasint" json:"share"`
	Users     []int64   `cbor:"4,keyasint" json:"users"`
	AutoAfter int       `cbor:"5,keyasint" json:"auto_after"`
	StartedAt time.Time `cbor:"6,keyasint" json:"started_at"`
	Running   bool      `cbor:"7,keyasint" json:"running"`
	PID       int       `cbor:"8,keyasint,omitempty" json:"pid,omitempty"`
	Port      int       `cbor:"9,keyasint" json:"port"`
}

type canaryRoute struct {
	port  int
	share int
	users map[int64]bool
}

type canaryRoutes struct {
	mu    sync.RWMutex
	m     map[int64]canaryRoute
	stats map[int64]*canaryStats
}

var errNoCanary = wire.Errorf(wire.CodeNotFound, "канарейки нет")

// route is the pipeline's Route: users of the canary go to its port.
func (c *Core) route(u *ingress.Update) (int, bool) {
	c.canaries.mu.RLock()
	r, ok := c.canaries.m[u.BotID]
	c.canaries.mu.RUnlock()
	if !ok || !ingress.ToCanary(u, r.share, r.users) {
		return 0, false
	}
	return r.port, true
}

func (c *Core) setRoute(botID int64, r *canaryRoute) {
	c.canaries.mu.Lock()
	defer c.canaries.mu.Unlock()
	if c.canaries.m == nil {
		c.canaries.m = map[int64]canaryRoute{}
	}
	if r == nil {
		delete(c.canaries.m, botID)
		return
	}
	c.canaries.m[botID] = *r
}

func userSet(ids []int64) map[int64]bool {
	m := map[int64]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// CanaryOf returns the canary of a bot, or nil.
func (c *Core) CanaryOf(ctx context.Context, botID int64) (*Canary, error) {
	var cn Canary
	var users, started string
	err := c.DB.R.QueryRowContext(ctx, `SELECT bot_id, version, share, users, auto_after, started_at FROM canaries WHERE bot_id = ?`, botID).
		Scan(&cn.BotID, &cn.Version, &cn.Share, &users, &cn.AutoAfter, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(users), &cn.Users)
	if cn.Users == nil {
		cn.Users = []int64{}
	}
	cn.StartedAt, _ = store.ParseTime(started)
	if b, info, ok := c.Runner.Canary(botID); ok {
		cn.Running, cn.PID, cn.Port = true, info.PID, b.Port
	}
	return &cn, nil
}

func validShare(s int) error {
	if s < 0 || s > 100 {
		return wire.Errorf(wire.CodeBadRequest, "доля от 0 до 100 %%")
	}
	return nil
}

// StartCanary starts the second instance with a new version.
func (c *Core) StartCanary(ctx context.Context, in CanaryReq) error {
	b, err := runner.LoadBot(ctx, c.DB, in.BotID)
	if err != nil {
		return errNoBot
	}
	if err := validShare(in.Share); err != nil {
		return err
	}
	if !c.Runner.IsRunning(in.BotID) {
		return wire.Errorf("conflict", "канарейка запускается рядом с работающим ботом")
	}
	if c.Maint.IsActive(ctx, in.BotID) {
		return wire.Errorf("conflict", "во время техработ и паузы канарейку запустить нельзя")
	}
	if in.Version == b.Version {
		return wire.Errorf(wire.CodeBadRequest, "версия %s уже основная", in.Version)
	}
	if !c.Releases.Exists(in.BotID, in.Version) {
		return wire.Errorf(wire.CodeNotFound, "нет версии %s", in.Version)
	}
	if cn, _ := c.CanaryOf(ctx, in.BotID); cn != nil {
		return wire.Errorf("conflict", "канарейка %s уже запущена", cn.Version)
	}
	users, _ := json.Marshal(nonNilIDs(in.Users))
	if _, err := c.DB.W.ExecContext(ctx, `INSERT INTO canaries (bot_id, version, share, users, auto_after, started_at, actor) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		in.BotID, in.Version, in.Share, string(users), in.AutoAfter, store.Now(), in.Actor.Source+":"+in.Actor.Name); err != nil {
		return err
	}
	cb := b
	cb.Version, cb.Port = in.Version, CanaryPort(b.Port)
	if err := c.Runner.StartCanary(ctx, cb); err != nil {
		c.DB.W.ExecContext(ctx, `DELETE FROM canaries WHERE bot_id = ?`, in.BotID)
		c.Proxy.Stop(in.BotID, "canary")
		return runnerError(err)
	}
	c.dropCanaryStats(in.BotID)
	c.canaryStatsOf(in.BotID)
	c.setRoute(in.BotID, &canaryRoute{port: cb.Port, share: in.Share, users: userSet(in.Users)})
	c.Ev.Emit("canary", in.BotID, events.Info, map[string]any{"action": "started", "version": in.Version, "share": in.Share, "users": len(in.Users)})
	return nil
}

func nonNilIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

// SetCanary changes who goes to the canary, on the fly.
func (c *Core) SetCanary(ctx context.Context, in CanaryReq) error {
	if err := validShare(in.Share); err != nil {
		return err
	}
	cn, err := c.CanaryOf(ctx, in.BotID)
	if err != nil {
		return err
	}
	if cn == nil {
		return errNoCanary
	}
	users, _ := json.Marshal(nonNilIDs(in.Users))
	if _, err := c.DB.W.ExecContext(ctx, `UPDATE canaries SET share = ?, users = ?, auto_after = ? WHERE bot_id = ?`,
		in.Share, string(users), in.AutoAfter, in.BotID); err != nil {
		return err
	}
	if cn.Running {
		c.setRoute(in.BotID, &canaryRoute{port: cn.Port, share: in.Share, users: userSet(in.Users)})
	} else {
		c.setRoute(in.BotID, nil) // not running: nothing to route to
	}
	return nil
}

// StopCanary stops the canary; all users go back to the main instance.
// The database is not touched.
func (c *Core) StopCanary(ctx context.Context, botID int64, reason string) error {
	cn, err := c.CanaryOf(ctx, botID)
	if err != nil || cn == nil {
		return err
	}
	c.setRoute(botID, nil)
	c.dropCanaryStats(botID)
	c.Runner.StopCanary(botID)
	c.Proxy.Stop(botID, "canary")
	c.DB.W.ExecContext(ctx, `DELETE FROM canaries WHERE bot_id = ?`, botID)
	c.Logs.Add(botID, "core", "canary "+cn.Version+" stopped: "+reason, nowUTC())
	c.Ev.Emit("canary", botID, events.Info, map[string]any{"action": "stopped", "version": cn.Version, "reason": reason})
	return nil
}

func (c *Core) registerCanary() {
	c.Pipe.Route = c.route
	c.Pipe.Observe = append(c.Pipe.Observe, c.observeCanary)
	// A canary does not outlive the core: users are back on the main
	// instance after a restart.
	if rows, err := c.DB.R.Query(`SELECT bot_id, version FROM canaries`); err == nil {
		type lost struct {
			id int64
			v  string
		}
		var ls []lost
		for rows.Next() {
			var l lost
			rows.Scan(&l.id, &l.v)
			ls = append(ls, l)
		}
		rows.Close()
		for _, l := range ls {
			c.DB.W.Exec(`DELETE FROM canaries WHERE bot_id = ?`, l.id)
			c.Ev.Emit("canary", l.id, events.Warn, map[string]any{"action": "stopped", "version": l.v, "reason": "рестарт ядра"})
		}
	}
	// The main instance stops → the canary too.
	c.onStopping = append(c.onStopping, func(ctx context.Context, b runner.Bot) {
		c.StopCanary(ctx, b.ID, "бот остановлен")
	})
	c.Mux.Handle("canary.get", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (*Canary, error) {
		if err := c.botExists(ctx, id); err != nil {
			return nil, err
		}
		return c.CanaryOf(ctx, id)
	}))
	c.Mux.Handle("canary.start", wire.Func(func(ctx context.Context, _ *wire.Conn, in CanaryReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "canary.start", in.BotID, map[string]any{"version": in.Version, "share": in.Share, "users": len(in.Users)}, func() error {
			return c.StartCanary(ctx, in)
		})
	}))
	c.Mux.Handle("canary.set", wire.Func(func(ctx context.Context, _ *wire.Conn, in CanaryReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "canary.set", in.BotID, map[string]any{"share": in.Share, "users": len(in.Users)}, func() error {
			return c.SetCanary(ctx, in)
		})
	}))
	c.Mux.Handle("canary.promote", wire.Func(func(ctx context.Context, _ *wire.Conn, in CanaryReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "canary.promote", in.BotID, nil, func() error {
			return c.PromoteCanary(ctx, in.BotID, in.Actor)
		})
	}))
	c.Mux.Handle("canary.stop", wire.Func(func(ctx context.Context, _ *wire.Conn, in CanaryReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "canary.stop", in.BotID, nil, func() error {
			reason := in.Reason
			if reason == "" {
				reason = "отменена"
			}
			return c.StopCanary(ctx, in.BotID, reason)
		})
	}))
}
