package core

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/safe"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// ScheduledDeploy is the latest one-time deploy plan for a bot.
type ScheduledDeploy struct {
	ID       int64     `cbor:"1,keyasint" json:"id"`
	Version  string    `cbor:"2,keyasint" json:"version"`
	StartsAt time.Time `cbor:"3,keyasint" json:"starts_at"`
	Status   string    `cbor:"4,keyasint" json:"status"`
	Error    string    `cbor:"5,keyasint,omitempty" json:"error,omitempty"`
}

type ScheduleDeploy struct {
	SwitchVersion
	StartsAt time.Time `cbor:"4,keyasint"`
}

type CancelScheduledDeploy struct {
	BotRef
	ID int64 `cbor:"2,keyasint"`
}

func (c *Core) scheduledDeploy(ctx context.Context, botID int64) (*ScheduledDeploy, error) {
	if err := c.botExists(ctx, botID); err != nil {
		return nil, err
	}
	var p ScheduledDeploy
	var startsAt string
	err := c.DB.R.QueryRowContext(ctx, `SELECT id, version, starts_at, status, error FROM scheduled_deploys WHERE bot_id = ?`, botID).
		Scan(&p.ID, &p.Version, &startsAt, &p.Status, &p.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.StartsAt, err = store.ParseTime(startsAt)
	return &p, err
}

func (c *Core) scheduleDeploy(ctx context.Context, in ScheduleDeploy) error {
	ctx, unlock, err := c.ConfigGuard.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := c.botExists(ctx, in.BotID); err != nil {
		return err
	}
	if !c.Releases.Exists(in.BotID, in.Version) {
		return wire.Errorf(wire.CodeNotFound, "нет версии %s", in.Version)
	}
	if err := c.releaseConfig(ctx, in.SwitchVersion, false); err != nil {
		return err
	}
	if !in.StartsAt.After(time.Now()) {
		return wire.Errorf(wire.CodeBadRequest, "укажите дату и время в будущем")
	}
	res, err := c.DB.W.ExecContext(ctx, `INSERT INTO scheduled_deploys (bot_id, version, starts_at, config_version) VALUES (?, ?, ?, ?)
		ON CONFLICT(bot_id) DO UPDATE SET id = excluded.id, version = excluded.version, starts_at = excluded.starts_at,
		config_version = excluded.config_version, status = 'scheduled', error = ''
		WHERE scheduled_deploys.status NOT IN ('scheduled', 'running')`, in.BotID, in.Version, store.FormatTime(in.StartsAt), in.ConfigVersion)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return wire.Errorf("conflict", "у бота уже есть запланированный или выполняющийся деплой")
	}
	return nil
}

func (c *Core) cancelScheduledDeploy(ctx context.Context, in CancelScheduledDeploy) error {
	res, err := c.DB.W.ExecContext(ctx, `DELETE FROM scheduled_deploys WHERE bot_id = ? AND id = ? AND status = 'scheduled'`, in.BotID, in.ID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return wire.Errorf("conflict", "этот план уже отменён или взят в работу")
	}
	return nil
}

func (c *Core) registerScheduledDeploys() {
	c.Mux.Handle("deploys.scheduled", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (*ScheduledDeploy, error) {
		return c.scheduledDeploy(ctx, id)
	}))
	c.Mux.Handle("deploys.schedule", wire.Func(func(ctx context.Context, _ *wire.Conn, in ScheduleDeploy) (struct{}, error) {
		args := map[string]any{"version": in.Version, "starts_at": in.StartsAt, "config_confirmed": in.ConfigVersion != ""}
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "deploys.schedule", in.BotID, args, func() error {
			return c.scheduleDeploy(ctx, in)
		})
	}))
	c.Mux.Handle("deploys.cancel", wire.Func(func(ctx context.Context, _ *wire.Conn, in CancelScheduledDeploy) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "deploys.cancel", in.BotID, map[string]int64{"id": in.ID}, func() error {
			return c.cancelScheduledDeploy(ctx, in)
		})
	}))
}

func (c *Core) runScheduledDeploys(ctx context.Context) {
	// An interrupted switch may have changed the bot already. Leave it for
	// the operator to inspect instead of repeating it after a core restart.
	if _, err := c.DB.W.ExecContext(ctx, `UPDATE scheduled_deploys SET status = 'failed', error = 'деплой прерван перезапуском ядра' WHERE status = 'running'`); err != nil {
		c.Log.Error("recovering scheduled deploys", "err", err)
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := c.tickScheduledDeploys(ctx, time.Now()); err != nil && ctx.Err() == nil {
			c.Log.Error("processing scheduled deploys", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Core) tickScheduledDeploys(ctx context.Context, now time.Time) error {
	rows, err := c.DB.R.QueryContext(ctx, `SELECT id FROM scheduled_deploys WHERE status = 'scheduled' AND starts_at <= ? ORDER BY starts_at, id`, store.FormatTime(now))
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := c.startScheduledDeploy(ctx, id); err != nil && ctx.Err() == nil {
			c.Log.Error("starting scheduled deploy", "id", id, "err", err)
		}
	}
	return ctx.Err()
}

func (c *Core) startScheduledDeploy(ctx context.Context, id int64) error {
	in := SwitchVersion{Actor: scheduleActor}
	err := c.DB.W.QueryRowContext(ctx, `UPDATE scheduled_deploys SET status = 'running' WHERE id = ? AND status = 'scheduled'
		RETURNING bot_id, version, config_version`, id).Scan(&in.BotID, &in.Version, &in.ConfigVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var deployID int64
	err = c.Journal.Do(ctx, in.Actor, "releases.switch", in.BotID, map[string]any{"version": in.Version, "schedule_id": id}, func() error {
		var err error
		deployID, err = c.Deploy(ctx, in)
		return err
	})
	if err != nil {
		if ctx.Err() == nil {
			status := "failed"
			var werr *wire.Error
			if errors.As(err, &werr) && werr.Code == "rolled_back" {
				status = "rolled_back"
			}
			c.finishScheduledDeploy(ctx, id, status, err.Error())
			if deployID == 0 {
				c.Ev.Emit("scheduled_deploy_failed", in.BotID, events.Error, map[string]any{"id": id, "version": in.Version, "error": err.Error()})
			}
		}
		return nil
	}
	if deployID == 0 {
		c.finishScheduledDeploy(ctx, id, "ok", "")
		return nil
	}
	safe.Go(c.Log, "observe scheduled deploy", func() {
		status := c.waitDeploy(ctx, deployID)
		if ctx.Err() != nil {
			return
		}
		var msg string
		if status != "ok" {
			c.DB.R.QueryRowContext(ctx, `SELECT error FROM deploys WHERE id = ?`, deployID).Scan(&msg)
		}
		c.finishScheduledDeploy(ctx, id, status, msg)
	})
	return nil
}

func (c *Core) finishScheduledDeploy(ctx context.Context, id int64, status, msg string) {
	if _, err := c.DB.W.ExecContext(ctx, `UPDATE scheduled_deploys SET status = ?, error = ? WHERE id = ? AND status = 'running'`, status, msg, id); err != nil && ctx.Err() == nil {
		c.Log.Error("finishing scheduled deploy", "id", id, "err", err)
	}
}
