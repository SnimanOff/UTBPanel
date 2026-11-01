package core

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// BackupRef names one backup.
type BackupRef struct {
	BotID int64         `cbor:"1,keyasint"`
	Name  string        `cbor:"2,keyasint,omitempty"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

func (c *Core) newBackups() *dbgate.Backups {
	return &dbgate.Backups{B: c.DBG, Dir: func(id int64) string {
		return filepath.Join(c.Cfg.DataDir, "backups", "bots", strconv.FormatInt(id, 10))
	}}
}

// BackupDB makes a copy of bot.db; a damaged copy raises an event.
func (c *Core) BackupDB(ctx context.Context, botID int64, reason string) (dbgate.Backup, error) {
	b, err := c.DBBackups.Make(ctx, botID, reason)
	if err == dbgate.ErrNoDB {
		return b, err
	}
	if err != nil {
		c.Ev.Emit("db_backup_failed", botID, events.Error, map[string]any{"name": b.Name, "error": err.Error()})
		return b, err
	}
	c.Logs.Add(botID, "core", "database backup "+b.Name+" ("+reason+")", nowUTC())
	return b, nil
}

// RestoreDB stops the bot, puts the backup in place and starts the bot
// again if it ran. Used by the owner and by autorollback.
func (c *Core) RestoreDB(ctx context.Context, botID int64, name string) error {
	if _, err := c.DBBackups.Path(botID, name); err != nil {
		return wire.Errorf(wire.CodeNotFound, "%v", err)
	}
	wasRunning := c.Runner.IsRunning(botID)
	if wasRunning {
		if err := c.Runner.Stop(ctx, botID); err != nil {
			return err
		}
	}
	var chown func(string) error
	if os.Geteuid() == 0 {
		chown = func(p string) error { return os.Chown(p, runner.UID(botID), runner.UID(botID)) }
	}
	err := c.DBBackups.Restore(botID, name, chown)
	if err == nil {
		c.Logs.Add(botID, "core", "database restored from "+name, nowUTC())
	}
	if wasRunning {
		if serr := c.StartBot(ctx, botID); err == nil {
			err = serr
		}
	}
	return err
}

// dailyBackups copies every bot database once a day.
func (c *Core) dailyBackups(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		bots, _ := c.Bots(ctx)
		for _, b := range bots {
			list, _ := c.DBBackups.List(b.ID)
			fresh := false
			for _, x := range list {
				if x.OK && time.Since(x.Created) < 24*time.Hour {
					fresh = true
					break
				}
			}
			if !fresh {
				c.BackupDB(ctx, b.ID, "daily")
			}
		}
	}
}

type fileChunks struct{ s *wire.Sender }

func (f fileChunks) Write(p []byte) (int, error) {
	if err := f.s.Send(append([]byte(nil), p...)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *Core) registerDBBackups() {
	c.Mux.Handle("db.checks", wire.Func(func(ctx context.Context, _ *wire.Conn, _ struct{}) ([]dbgate.Check, error) {
		return c.DBChecks.Last(ctx), nil
	}))
	c.Mux.Handle("db.backups", wire.Func(func(_ context.Context, _ *wire.Conn, id int64) ([]dbgate.Backup, error) {
		return c.DBBackups.List(id)
	}))
	c.Mux.Handle("db.backup.make", wire.Func(func(ctx context.Context, _ *wire.Conn, in BackupRef) (b dbgate.Backup, err error) {
		err = c.Journal.Do(ctx, in.Actor, "db.backup", in.BotID, nil, func() error {
			b, err = c.BackupDB(ctx, in.BotID, "manual")
			if err == dbgate.ErrNoDB {
				return wire.Errorf(wire.CodeNotFound, "у бота ещё нет базы данных")
			}
			return err
		})
		return b, err
	}))
	c.Mux.Handle("db.backup.restore", wire.Func(func(ctx context.Context, _ *wire.Conn, in BackupRef) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "db.restore", in.BotID, map[string]string{"backup": in.Name}, func() error {
			return c.RestoreDB(ctx, in.BotID, in.Name)
		})
	}))
	c.Mux.HandleStream("db.backup.download", func(ctx context.Context, _ *wire.Conn, body wire.Raw, s *wire.Sender) error {
		var in BackupRef
		if err := wire.Unmarshal(body, &in); err != nil {
			return err
		}
		p, err := c.DBBackups.Path(in.BotID, in.Name)
		if err != nil {
			return wire.Errorf(wire.CodeNotFound, "%v", err)
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.CopyBuffer(fileChunks{s}, f, make([]byte, 256<<10))
		return err
	})
}

// newChecks checks core.db and every bot.db with quick_check weekly.
func (c *Core) newChecks() *dbgate.Checks {
	return &dbgate.Checks{DB: c.DB, Table: "kv",
		Run: func(ctx context.Context) []dbgate.Check {
			out := []dbgate.Check{dbgate.CheckFile(ctx, "core.db", c.DB.Path, 0)}
			bots, _ := c.Bots(ctx)
			for _, b := range bots {
				p := c.DBG.Path(b.ID)
				if _, err := os.Stat(p); err == nil {
					out = append(out, dbgate.CheckFile(ctx, "bot.db "+b.Name, p, b.ID))
				}
			}
			return out
		},
		OnBad: func(ch dbgate.Check) {
			c.Ev.Emit("db_check_failed", ch.BotID, events.Error, map[string]any{"db": ch.Name, "error": ch.Error})
		},
	}
}
