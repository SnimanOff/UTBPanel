package core

import (
	"context"
	"encoding/json"
	"time"
	_ "time/tzdata" // zones are built in: the image has no system zone files guaranteed

	"github.com/SnimanOff/UTBPanel/backend/internal/edge"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/maintenance"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// MaintReq is a pause or maintenance request.
type MaintReq struct {
	BotID int64         `cbor:"1,keyasint"`
	Text  string        `cbor:"2,keyasint,omitempty"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

// BotTZ is the zone of texts for users of a bot: Europe/Moscow unless the
// owner chose another.
func (c *Core) BotTZ(ctx context.Context, botID int64) *time.Location {
	name := DefaultTZ
	if st, err := c.Settings(ctx, botID); err == nil {
		var tz string
		if json.Unmarshal(st["tz"], &tz) == nil && tz != "" {
			name = tz
		}
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc, _ = time.LoadLocation(DefaultTZ)
	}
	return loc
}

func (c *Core) newMaintenance() *maintenance.Service {
	return &maintenance.Service{DB: c.DB, TZ: c.BotTZ, Changed: c.maintChanged}
}

// maintChanged tells the edge, updates the state and, at the end, sends the
// deferred updates to the bot.
func (c *Core) maintChanged(botID int64, m *maintenance.M, ended bool) {
	ctx := context.Background()
	go c.PushEdge(ctx)
	if ended {
		if c.Runner.IsRunning(botID) {
			c.DB.W.ExecContext(ctx, `UPDATE bots SET state = 'running' WHERE id = ? AND state = 'paused'`, botID)
		}
		c.Logs.Add(botID, "core", "maintenance ended", nowUTC())
		c.Ev.Emit("maintenance_ended", botID, events.Info, map[string]any{"kind": m.Kind})
		go c.FlushDeferred(ctx, botID)
		return
	}
	if m.State == maintenance.Active {
		c.DB.W.ExecContext(ctx, `UPDATE bots SET state = 'paused' WHERE id = ? AND state = 'running'`, botID)
		c.Logs.Add(botID, "core", "maintenance started ("+m.Kind+")", nowUTC())
		c.Ev.Emit("maintenance_started", botID, events.Info, map[string]any{"kind": m.Kind, "ends_at": m.EndsAt})
	}
}

// maintLabel is shown on cards: "пауза" or "техработы 18:00–19:00".
func (c *Core) maintLabel(ctx context.Context, botID int64) string {
	m, ok := c.Maint.Get(botID)
	if !ok {
		return ""
	}
	return maintenance.Label(m, c.BotTZ(ctx, botID))
}

func (c *Core) registerMaintenance() {
	c.pausedChecks = append(c.pausedChecks, c.Maint.IsActive)
	// The edge answers by the same rules while the core is down.
	c.edge.extra = append(c.edge.extra, func(ctx context.Context, id int64, b *edge.BotSettings) {
		if m, ok := c.Maint.Get(id); ok && m.State == maintenance.Active {
			b.Maintenance, b.MaintenanceText = true, c.Maint.Text(ctx, m)
		}
		b.TZ = c.BotTZ(ctx, id).String()
	})
	c.Mux.Handle("maintenance.get", wire.Func(func(_ context.Context, _ *wire.Conn, id int64) (*maintenance.M, error) {
		m, ok := c.Maint.Get(id)
		if !ok {
			return nil, nil
		}
		return &m, nil
	}))
	c.Mux.Handle("maintenance.pause", wire.Func(func(ctx context.Context, _ *wire.Conn, in MaintReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "bots.pause", in.BotID, nil, func() error {
			if err := c.botExists(ctx, in.BotID); err != nil {
				return err
			}
			if err := c.Maint.PauseBot(ctx, in.BotID, in.Text); err != nil {
				return wire.Errorf("conflict", "у бота уже идут техработы")
			}
			return nil
		})
	}))
	c.Mux.Handle("maintenance.end", wire.Func(func(ctx context.Context, _ *wire.Conn, in MaintReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "bots.resume", in.BotID, nil, func() error {
			return c.Maint.End(ctx, in.BotID)
		})
	}))
}
