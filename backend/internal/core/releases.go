package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// UploadBegin starts a release upload.
type UploadBegin struct {
	BotID   int64         `cbor:"1,keyasint"`
	Version string        `cbor:"2,keyasint,omitempty"`
	Source  string        `cbor:"3,keyasint,omitempty"`
	Actor   journal.Actor `cbor:"9,keyasint"`
}

// UploadChunk is one piece of the binary (at most 256 KB).
type UploadChunk struct {
	ID   string `cbor:"1,keyasint"`
	Data []byte `cbor:"2,keyasint"`
}

// UploadFinish ends an upload.
type UploadFinish struct {
	ID    string        `cbor:"1,keyasint"`
	BotID int64         `cbor:"2,keyasint"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

// SwitchVersion makes a stored version current.
type SwitchVersion struct {
	BotID   int64         `cbor:"1,keyasint"`
	Version string        `cbor:"2,keyasint"`
	Actor   journal.Actor `cbor:"9,keyasint"`
}

func (c *Core) newReleases() *deploy.Store {
	s := &deploy.Store{Dir: func(id int64) string {
		return filepath.Join(c.Cfg.DataDir, "bots", strconv.FormatInt(id, 10), "releases")
	}}
	if c.Runner != nil && os.Geteuid() == 0 {
		s.Chown = func(id int64, p string) error { return os.Chown(p, runner.UID(id), runner.UID(id)) }
	}
	return s
}

func checkErr(err error) error {
	if deploy.IsCheckError(err) {
		return wire.Errorf("bad_binary", "%v", err)
	}
	if errors.Is(err, deploy.ErrUnknownUpload) {
		return wire.Errorf(wire.CodeNotFound, "upload expired, start again")
	}
	return err
}

func (c *Core) versions(ctx context.Context, id int64) (cur, prev string, err error) {
	err = c.DB.R.QueryRowContext(ctx, `SELECT current_version, previous_version FROM bots WHERE id = ?`, id).Scan(&cur, &prev)
	return
}

// SwitchVersion stops the bot, makes version current and starts it again
// if it was running. Backup, observation and autorollback come on top.
func (c *Core) SwitchVersion(ctx context.Context, in SwitchVersion) error {
	b, err := c.Bot(ctx, in.BotID)
	if err != nil {
		return err
	}
	if !c.Releases.Exists(in.BotID, in.Version) {
		return wire.Errorf(wire.CodeNotFound, "no version %s", in.Version)
	}
	if b.Version == in.Version {
		return nil
	}
	// A copy of the database before every deploy.
	if _, err := c.BackupDB(ctx, in.BotID, "deploy"); err != nil && err != dbgate.ErrNoDB {
		return wire.Errorf("backup_failed", "бэкап базы перед деплоем не удался: %v", err)
	}
	wasRunning := c.Runner.IsRunning(in.BotID)
	if wasRunning {
		if err := c.Runner.Stop(ctx, in.BotID); err != nil {
			return err
		}
	}
	if _, err := c.DB.W.ExecContext(ctx, `UPDATE bots SET previous_version = current_version, current_version = ? WHERE id = ?`, in.Version, in.BotID); err != nil {
		return err
	}
	c.Logs.Add(in.BotID, "core", "version "+b.Version+" → "+in.Version, nowUTC())
	if wasRunning || b.Version == "" {
		if err := c.StartBot(ctx, in.BotID); err != nil {
			return err
		}
	}
	cur, prev, _ := c.versions(ctx, in.BotID)
	c.Releases.Prune(in.BotID, cur, prev)
	return nil
}

func (c *Core) registerReleases() {
	c.Mux.Handle("releases.begin", wire.Func(func(ctx context.Context, _ *wire.Conn, in UploadBegin) (string, error) {
		if err := c.botExists(ctx, in.BotID); err != nil {
			return "", err
		}
		if in.Source == "" {
			in.Source = "upload"
		}
		id, err := c.Releases.Begin(in.BotID, in.Version, in.Source)
		return id, checkErr(err)
	}))
	c.Mux.Handle("releases.chunk", wire.Func(func(_ context.Context, _ *wire.Conn, in UploadChunk) (struct{}, error) {
		return struct{}{}, checkErr(c.Releases.Write(in.ID, in.Data))
	}))
	c.Mux.Handle("releases.abort", wire.Func(func(_ context.Context, _ *wire.Conn, id string) (struct{}, error) {
		c.Releases.Abort(id)
		return struct{}{}, nil
	}))
	c.Mux.Handle("releases.finish", wire.Func(func(ctx context.Context, _ *wire.Conn, in UploadFinish) (r deploy.Release, err error) {
		err = c.Journal.Do(ctx, in.Actor, "releases.upload", in.BotID, nil, func() error {
			r, err = c.Releases.Finish(in.ID)
			return checkErr(err)
		})
		if err == nil {
			cur, prev, _ := c.versions(ctx, in.BotID)
			c.Releases.Prune(in.BotID, cur, prev)
			c.Logs.Add(in.BotID, "core", "uploaded version "+r.Version, nowUTC())
		}
		return r, err
	}))
	c.Mux.Handle("releases.list", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) ([]deploy.Release, error) {
		cur, prev, err := c.versions(ctx, id)
		if err != nil {
			return nil, errNoBot
		}
		return c.Releases.List(id, cur, prev)
	}))
	c.Mux.Handle("releases.switch", wire.Func(func(ctx context.Context, _ *wire.Conn, in SwitchVersion) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "releases.switch", in.BotID, map[string]string{"version": in.Version}, func() error {
			return c.SwitchVersion(ctx, in)
		})
	}))
}
