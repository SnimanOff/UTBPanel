package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

func spool(dir, name string, fill func(io.Writer) error) error {
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := fill(f); err != nil {
		return err
	}
	return f.Close()
}

// ExportBot never reads the secrets table's values or the runtime secrets/
// directory. The archive is encrypted before being written to persistent disk.
func (c *Core) ExportBot(ctx context.Context, in backup.ExportRequest, metrics io.Reader) (backup.File, error) {
	if err := backup.ValidatePassword(in.Password); err != nil {
		return backup.File{}, wire.Errorf(wire.CodeBadRequest, "%v", err)
	}
	locked, unlock, err := c.ConfigGuard.Lock(ctx)
	if err != nil {
		return backup.File{}, err
	}
	snap, err := c.ConfigSnapshot(locked, in.BotID)
	if err != nil {
		unlock()
		return backup.File{}, err
	}
	d := in.Config.Document
	d.Version = ""
	d.Group = nil
	d.Alerts = []botconfig.Alert{}
	if in.Config.Version != snap.Version || botconfig.Fingerprint(d) != botconfig.Fingerprint(snap.Document) {
		unlock()
		return backup.File{}, wire.Errorf("conflict", "настройки изменились; повторите экспорт")
	}
	b, err := c.Bot(locked, in.BotID)
	unlock()
	if err != nil {
		return backup.File{}, err
	}
	if in.Options.Metrics && (in.Metrics == nil || metrics == nil || in.Metrics.BotID != in.BotID || in.Metrics.Size < 0) {
		return backup.File{}, wire.Errorf(wire.CodeBadRequest, "missing metrics snapshot")
	}
	// Drain the tracker before taking one read transaction for both exports.
	if err := c.Users.Flush(ctx); err != nil {
		return backup.File{}, err
	}
	tmp, err := os.MkdirTemp("", "utbpanel-export-*")
	if err != nil {
		return backup.File{}, err
	}
	defer os.RemoveAll(tmp)
	tx, err := c.DB.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return backup.File{}, err
	}
	defer tx.Rollback()
	if err := spool(tmp, "users.json", func(w io.Writer) error {
		if _, err := io.WriteString(w, `{"version":1,"users":`); err != nil {
			return err
		}
		if err := backup.Rows(ctx, tx, w, `SELECT b.*,COALESCE(g.username,'') AS username,COALESCE(g.language_code,'') AS language_code,COALESCE(g.is_premium,0) AS is_premium,COALESCE(g.updated_at,'') AS profile_updated_at FROM bot_users b LEFT JOIN tg_users g ON g.user_id=b.user_id WHERE b.bot_id=? ORDER BY b.user_id`, in.BotID); err != nil {
			return err
		}
		if _, err := io.WriteString(w, `,"allow":`); err != nil {
			return err
		}
		if err := backup.Rows(ctx, tx, w, `SELECT user_id,note FROM bot_allow WHERE bot_id=? ORDER BY user_id`, in.BotID); err != nil {
			return err
		}
		_, err := io.WriteString(w, "}")
		return err
	}); err != nil {
		return backup.File{}, err
	}
	if err := spool(tmp, "payments.json", func(w io.Writer) error {
		if _, err := io.WriteString(w, `{"version":1,"payments":`); err != nil {
			return err
		}
		if err := backup.Rows(ctx, tx, w, `SELECT * FROM payments WHERE bot_id=? ORDER BY id`, in.BotID); err != nil {
			return err
		}
		_, err := io.WriteString(w, "}")
		return err
	}); err != nil {
		return backup.File{}, err
	}
	if err := tx.Commit(); err != nil {
		return backup.File{}, err
	}
	root, err := os.OpenRoot(tmp)
	if err != nil {
		return backup.File{}, err
	}
	defer root.Close()
	botDir := filepath.Join(c.Cfg.DataDir, "bots", strconv.FormatInt(in.BotID, 10))
	var botRoot *os.Root
	if botRoot, err = os.OpenRoot(botDir); err != nil && !os.IsNotExist(err) {
		return backup.File{}, err
	}
	if botRoot != nil {
		defer botRoot.Close()
	}
	// SQLite needs a real snapshot file. It lives in a private temporary
	// directory accessible only to this bot UID, and is removed on every exit.
	dbDir, err := os.MkdirTemp("", "utbpanel-export-db-*")
	if err != nil {
		return backup.File{}, err
	}
	defer os.RemoveAll(dbDir)
	uid := -1
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		uid = runner.UID(in.BotID)
		if err := os.Chown(dbDir, uid, uid); err != nil {
			return backup.File{}, err
		}
	}
	err = dbgate.Snapshot(ctx, c.DBG.Path(in.BotID), filepath.Join(dbDir, "bot.db"), uid)
	hasDB := err == nil
	if err != nil && !errors.Is(err, dbgate.ErrNoDB) {
		return backup.File{}, err
	}
	dbRoot, err := os.OpenRoot(dbDir)
	if err != nil {
		return backup.File{}, err
	}
	defer dbRoot.Close()
	manifest := backup.Manifest{Name: b.Name, Username: b.Username, TelegramID: b.TgID, CurrentVersion: b.Version, Group: in.Config.Document.Group, Releases: []deploy.Release{}}
	if in.Options.AllVersions {
		manifest.Releases, err = c.Releases.List(in.BotID, b.Version, "")
		if err != nil {
			return backup.File{}, err
		}
	} else if b.Version != "" {
		rel, err := c.Releases.Get(in.BotID, b.Version)
		if err != nil {
			return backup.File{}, err
		}
		rel.Current = true
		manifest.Releases = append(manifest.Releases, rel)
	}
	if b.Version != "" {
		found := false
		for _, rel := range manifest.Releases {
			if rel.Version == b.Version {
				found = true
			}
		}
		if !found {
			return backup.File{}, fmt.Errorf("current binary is missing")
		}
	}
	for _, rel := range manifest.Releases {
		if !deploy.ValidVersion(rel.Version) {
			return backup.File{}, fmt.Errorf("invalid stored release version")
		}
	}
	config := in.Config.Document
	config.Version = ""
	raw, err := botconfig.YAML(config)
	if err != nil {
		return backup.File{}, err
	}
	return c.Exports.Create(ctx, in.BotID, backup.Filename(b.Name, b.ID), func(out io.Writer) error {
		return backup.Encrypt(ctx, out, in.Password, manifest, func(a *backup.Archive) error {
			if err := a.Bytes("config.yaml", []byte(raw)); err != nil {
				return err
			}
			for _, name := range []string{"users.json", "payments.json"} {
				if err := a.RootFile(root, name, name, false); err != nil {
					return err
				}
			}
			if hasDB {
				if err := a.RootFile(dbRoot, "bot.db", "bot.db", false); err != nil {
					return err
				}
			}
			for _, rel := range manifest.Releases {
				if botRoot == nil {
					return fmt.Errorf("binary directory missing")
				}
				if err := a.RootFile(botRoot, path.Join("releases", rel.Version, "bot"), path.Join("bin", rel.Version, "bot"), true); err != nil {
					return err
				}
			}
			if in.Options.Files && botRoot != nil {
				if _, err := botRoot.Lstat("files"); err == nil {
					if err := a.Tree(botRoot, "files", "files"); err != nil {
						return err
					}
				} else if !os.IsNotExist(err) {
					return err
				}
			}
			if in.Options.Metrics {
				if err := a.Add("metrics/history.json", in.Metrics.Size, metrics); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func (c *Core) registerExports() {
	c.Exports = &backup.Files{Dir: filepath.Join(c.Cfg.DataDir, "exports")}
	c.Exports.Prune()
	c.Mux.Handle("exports.bot", wire.Func(func(ctx context.Context, conn *wire.Conn, in backup.ExportRequest) (backup.File, error) {
		var reader io.Reader
		if in.Metrics != nil {
			reader = &backup.RemoteReader{Ctx: ctx, Ref: backup.FileRef{ID: in.Metrics.ID, BotID: in.BotID}, Size: in.Metrics.Size, ReadChunk: func(ctx context.Context, ref backup.FileRef) ([]byte, error) {
				var chunk []byte
				err := conn.Call(ctx, "exports.metrics.read", ref, &chunk)
				return chunk, err
			}}
		}
		var out backup.File
		err := c.Journal.Do(ctx, in.Actor, "bot.export", in.BotID, map[string]any{"files": in.Options.Files, "metrics": in.Options.Metrics, "all_versions": in.Options.AllVersions}, func() error { var err error; out, err = c.ExportBot(ctx, in, reader); return err })
		return out, err
	}))
	c.Mux.Handle("exports.info", wire.Func(func(_ context.Context, _ *wire.Conn, ref backup.FileRef) (backup.File, error) {
		out, err := c.Exports.Info(ref)
		if err != nil {
			return out, wire.Errorf(wire.CodeNotFound, "%v", err)
		}
		return out, nil
	}))
	c.Mux.Handle("exports.read", wire.Func(func(_ context.Context, _ *wire.Conn, ref backup.FileRef) ([]byte, error) { return c.Exports.Read(ref) }))
	c.Mux.Handle("exports.delete", wire.Func(func(_ context.Context, _ *wire.Conn, ref backup.FileRef) (struct{}, error) {
		return struct{}{}, c.Exports.Delete(ref)
	}))
}
