package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/edge"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/maintenance"
	"github.com/SnimanOff/UTBPanel/backend/internal/tztext"
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
	var tz string
	if st, err := c.Settings(ctx, botID); err == nil {
		json.Unmarshal(st["tz"], &tz)
	}
	return tztext.Load(tz)
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
		if c.SDKTasks != nil {
			c.SDKTasks.CancelBot(botID)
		}
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
			// The edge renders again when it answers: "сегодня" changes.
			b.MaintenanceTemplate, b.StartsAt, b.EndsAt = maintenance.Template(m), m.StartsAt, m.EndsAt
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

// ScheduleReq plans maintenance.
type ScheduleReq struct {
	Plan    maintenance.Plan `cbor:"1,keyasint"`
	Minutes int              `cbor:"2,keyasint,omitempty"` // extend
	Actor   journal.Actor    `cbor:"9,keyasint"`
}

var scheduleActor = journal.Actor{Source: journal.SourceSchedule, Name: "schedule"}

// runSchedule starts planned maintenance, deploys the chosen version and
// notices overdue maintenance.
func (c *Core) runSchedule(ctx context.Context) {
	c.Maint.Run(ctx, maintenance.Hooks{
		Deploy: func(ctx context.Context, botID int64, version string) error {
			var id int64
			err := c.Journal.Do(ctx, scheduleActor, "releases.switch", botID, map[string]string{"version": version}, func() error {
				var err error
				id, err = c.Deploy(ctx, SwitchVersion{BotID: botID, Version: version, Actor: scheduleActor})
				return err
			})
			if err != nil {
				return err
			}
			// Maintenance ends only after the observation; on rollback it stays.
			if c.waitDeploy(ctx, id) == "rolled_back" {
				return fmt.Errorf("версия %s откачена автооткатом", version)
			}
			return nil
		},
		Event: func(botID int64, typ string, data map[string]any) {
			c.Ev.Emit(typ, botID, events.Warn, data)
		},
		Warn: c.maintWarn,
	})
}

func (c *Core) registerSchedule() {
	c.Mux.Handle("maintenance.schedule", wire.Func(func(ctx context.Context, _ *wire.Conn, in ScheduleReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "maintenance.schedule", in.Plan.BotID, in.Plan, func() error {
			if err := c.botExists(ctx, in.Plan.BotID); err != nil {
				return err
			}
			if in.Plan.Version != "" && !c.Releases.Exists(in.Plan.BotID, in.Plan.Version) {
				return wire.Errorf(wire.CodeNotFound, "нет версии %s", in.Plan.Version)
			}
			switch err := c.Maint.Schedule(ctx, in.Plan); err {
			case nil:
				return nil
			case maintenance.ErrTimes:
				return wire.Errorf(wire.CodeBadRequest, "окончание должно быть позже начала")
			case maintenance.ErrPast:
				return wire.Errorf(wire.CodeBadRequest, "окончание уже прошло")
			case maintenance.ErrActive:
				return wire.Errorf("conflict", "у бота уже идут техработы")
			default:
				return err
			}
		})
	}))
	c.Mux.Handle("maintenance.extend", wire.Func(func(ctx context.Context, _ *wire.Conn, in ScheduleReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "maintenance.extend", in.Plan.BotID, map[string]int{"minutes": in.Minutes}, func() error {
			if in.Minutes <= 0 || in.Minutes > 24*60 {
				return wire.Errorf(wire.CodeBadRequest, "продлить можно на 1–1440 минут")
			}
			return c.Maint.Extend(ctx, in.Plan.BotID, time.Duration(in.Minutes)*time.Minute)
		})
	}))
}

// maintWarn sends the early warning; it works once broadcasts exist.
func (c *Core) maintWarn(ctx context.Context, m maintenance.M, text string) error {
	if c.warnHook == nil {
		return errNoBroadcast
	}
	return c.warnHook(ctx, m.BotID, text)
}

var errNoBroadcast = wire.Errorf("unavailable", "broadcasts are not available yet")
