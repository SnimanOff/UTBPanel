package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
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

// SwitchVersion makes a stored version current. ConfigVersion confirms the
// diff of the release's tgpanel.yaml made against that settings revision.
type SwitchVersion struct {
	BotID         int64         `cbor:"1,keyasint"`
	Version       string        `cbor:"2,keyasint"`
	ConfigVersion string        `cbor:"3,keyasint,omitempty"`
	Actor         journal.Actor `cbor:"9,keyasint"`
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
// if it was running. The deploy is recorded with the database backup made
// before it; observation and autorollback come on top.
func (c *Core) SwitchVersion(ctx context.Context, in SwitchVersion) error {
	_, err := c.Deploy(ctx, in)
	return err
}

// Deploy is SwitchVersion that returns the deploy record (0 when nothing
// changed).
func (c *Core) Deploy(ctx context.Context, in SwitchVersion) (int64, error) {
	b, err := c.Bot(ctx, in.BotID)
	if err != nil {
		return 0, err
	}
	if !c.Releases.Exists(in.BotID, in.Version) {
		return 0, wire.Errorf(wire.CodeNotFound, "no version %s", in.Version)
	}
	if b.Version == in.Version {
		return 0, nil
	}
	// Settings first: env and limits take effect when the new version starts.
	if err := c.applyReleaseConfig(ctx, in); err != nil {
		return 0, err
	}
	// A copy of the database before every deploy.
	bk, err := c.BackupDB(ctx, in.BotID, "deploy")
	if err != nil && err != dbgate.ErrNoDB {
		return 0, wire.Errorf("backup_failed", "бэкап базы перед деплоем не удался: %v", err)
	}
	res, err := c.DB.W.ExecContext(ctx, `INSERT INTO deploys (bot_id, from_version, to_version, backup, schema_before, actor, started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		in.BotID, b.Version, in.Version, bk.Name, bk.SchemaVersion, in.Actor.Source+":"+in.Actor.Name, store.Now())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	fail := func(err error) (int64, error) {
		c.finishDeploy(id, in.BotID, b.Version, in.Version, "failed", err.Error())
		return id, err
	}
	wasRunning := c.Runner.IsRunning(in.BotID)
	if wasRunning {
		if err := c.Runner.Stop(ctx, in.BotID); err != nil {
			return fail(err)
		}
	}
	if _, err := c.DB.W.ExecContext(ctx, `UPDATE bots SET previous_version = current_version, current_version = ? WHERE id = ?`, in.Version, in.BotID); err != nil {
		return fail(err)
	}
	c.Logs.Add(in.BotID, "core", "version "+b.Version+" → "+in.Version, nowUTC())
	started := wasRunning || b.Version == ""
	if started {
		if err := c.StartBot(ctx, in.BotID); err != nil {
			if b.Version != "" && in.Actor.Source != journal.SourceAutorollback {
				// The new version did not start: back to the old one at once.
				c.rollbackNow(id, in.BotID, b.Version, in.Version, bk.Name, bk.SchemaVersion, "не запустилась: "+err.Error())
				return id, wire.Errorf("rolled_back", "версия %s не запустилась, возвращена %s: %v", in.Version, b.Version, err)
			}
			return fail(err)
		}
	}
	cur, prev, _ := c.versions(ctx, in.BotID)
	c.Releases.Prune(in.BotID, cur, prev)
	if started && b.Version != "" && in.Actor.Source != journal.SourceAutorollback {
		// The record stays "observing" for a minute; autorollback decides.
		c.Ev.Emit("deploy", in.BotID, events.Info, map[string]any{"id": id, "from": b.Version, "version": in.Version, "result": "observing"})
		c.watchDeploy(id, in.BotID, b.Version, in.Version, bk.Name, bk.SchemaVersion)
		return id, nil
	}
	c.finishDeploy(id, in.BotID, b.Version, in.Version, "ok", "")
	return id, nil
}

// finishDeploy closes the record and tells the panel (marks on charts).
func (c *Core) finishDeploy(id, botID int64, from, to, result, msg string) {
	c.DB.W.ExecContext(context.Background(), `UPDATE deploys SET result = ?, error = ?, finished_at = ? WHERE id = ?`, result, msg, store.Now(), id)
	sev := events.Info
	if result != "ok" {
		sev = events.Error
	}
	c.Ev.Emit("deploy", botID, sev, map[string]any{"id": id, "from": from, "version": to, "result": result, "error": msg})
}

// DeployRecord is one row of the deploy history.
type DeployRecord struct {
	ID         int64     `cbor:"1,keyasint" json:"id"`
	From       string    `cbor:"2,keyasint" json:"from"`
	To         string    `cbor:"3,keyasint" json:"to"`
	Backup     string    `cbor:"4,keyasint" json:"backup,omitempty"`
	Actor      string    `cbor:"5,keyasint" json:"actor"`
	StartedAt  time.Time `cbor:"6,keyasint" json:"started_at"`
	FinishedAt time.Time `cbor:"7,keyasint,omitzero" json:"finished_at,omitzero"`
	Result     string    `cbor:"8,keyasint" json:"result"`
	Error      string    `cbor:"9,keyasint,omitempty" json:"error,omitempty"`
}

// Deploys returns the newest deploys of a bot.
func (c *Core) Deploys(ctx context.Context, botID int64, limit int) ([]DeployRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	rows, err := c.DB.R.QueryContext(ctx, `SELECT id, from_version, to_version, backup, actor, started_at, COALESCE(finished_at, ''), result, error
		FROM deploys WHERE bot_id = ? ORDER BY id DESC LIMIT ?`, botID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeployRecord{}
	for rows.Next() {
		var d DeployRecord
		var st, fin string
		if err := rows.Scan(&d.ID, &d.From, &d.To, &d.Backup, &d.Actor, &st, &fin, &d.Result, &d.Error); err != nil {
			return nil, err
		}
		d.StartedAt, _ = store.ParseTime(st)
		d.FinishedAt, _ = store.ParseTime(fin)
		out = append(out, d)
	}
	return out, rows.Err()
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
	c.Mux.Handle("deploys.list", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) ([]DeployRecord, error) {
		return c.Deploys(ctx, id, 30)
	}))
	c.Mux.Handle("releases.switch", wire.Func(func(ctx context.Context, _ *wire.Conn, in SwitchVersion) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "releases.switch", in.BotID, map[string]string{"version": in.Version}, func() error {
			return c.SwitchVersion(ctx, in)
		})
	}))
}
