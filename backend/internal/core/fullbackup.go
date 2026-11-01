package core

import (
	"archive/tar"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/buildinfo"
	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

type FullBackupRequest struct {
	Files *bool         `cbor:"1,keyasint,omitempty"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

const fullConfigKey = "full_backup_settings"
const fullAutoKey = "full_backup_auto"
const fullErrorKey = "full_backup_error"
const fullDownloadKey = "full_backup_download"

func (c *Core) backupValue(ctx context.Context, key string) string {
	var value string
	c.DB.R.QueryRowContext(ctx, `SELECT value FROM kv WHERE key=?`, key).Scan(&value)
	return value
}
func (c *Core) putBackupValue(ctx context.Context, key, value string) error {
	_, err := c.DB.W.ExecContext(ctx, `INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}
func (c *Core) fullSettings(ctx context.Context) (backup.FullSettings, error) {
	cfg := backup.DefaultFullSettings()
	if raw := c.backupValue(ctx, fullConfigKey); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return cfg, err
		}
	}
	return cfg, cfg.Validate()
}
func (c *Core) setFullSettings(ctx context.Context, cfg backup.FullSettings) error {
	if err := cfg.Validate(); err != nil {
		return wire.Errorf(wire.CodeBadRequest, "%v", err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := c.DB.Tx(ctx, func(tx *sql.Tx) error {
		for key, value := range map[string]string{fullConfigKey: string(raw), fullAutoKey: time.Now().UTC().Format(time.RFC3339Nano)} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return backup.Retain(c.FullBackups, cfg.Keep)
}
func (c *Core) fullStatus(ctx context.Context) (backup.FullStatus, error) {
	cfg, err := c.fullSettings(ctx)
	if err != nil {
		return backup.FullStatus{}, err
	}
	files, err := c.FullBackups.List()
	if err != nil {
		return backup.FullStatus{}, err
	}
	out := backup.FullStatus{Files: files, Settings: cfg, Busy: c.backupBusy.Load(), LastError: c.backupValue(ctx, fullErrorKey)}
	out.LastDownload, _ = time.Parse(time.RFC3339Nano, c.backupValue(ctx, fullDownloadKey))
	if cfg.Enabled {
		last, err := time.Parse(time.RFC3339Nano, c.backupValue(ctx, fullAutoKey))
		if err != nil {
			last = time.Now()
		}
		out.Next = cfg.Next(last)
	}
	return out, nil
}

func addPanelBackup(ctx context.Context, a *backup.Archive, input io.Reader) error {
	reader := tar.NewReader(input)
	total := int64(0)
	count := 0
	seen := map[string]bool{}
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		count++
		if count > 100_000 || h.Typeflag != tar.TypeReg || (h.Name != "panel.db" && !strings.HasPrefix(h.Name, "pki/")) || h.Size < 0 || h.Size > backup.MaxImportBytes-total || seen[h.Name] {
			return fmt.Errorf("invalid panel backup part")
		}
		seen[h.Name] = true
		total += h.Size
		if err := a.Add(h.Name, h.Size, reader); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	for _, name := range []string{"panel.db", "pki/ca.key", "pki/ca.crt"} {
		if !seen[name] {
			return fmt.Errorf("panel backup lacks %s", name)
		}
	}
	return nil
}

func (c *Core) addFullBot(ctx context.Context, a *backup.Archive, b backup.FullBot, files bool) error {
	dir := filepath.Join(c.Cfg.DataDir, "bots", strconv.FormatInt(b.ID, 10))
	root, err := os.OpenRoot(dir)
	if os.IsNotExist(err) && b.Version == "" {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	prefix := path.Join("bots", strconv.FormatInt(b.ID, 10))
	if b.Version != "" {
		if !deploy.ValidVersion(b.Version) {
			return fmt.Errorf("invalid release version")
		}
		for _, name := range []string{"bot", "release.json"} {
			relative := path.Join("releases", b.Version, name)
			if err := a.RootFile(root, relative, path.Join(prefix, relative), name == "bot"); err != nil {
				return err
			}
		}
	}
	snapshot, err := os.MkdirTemp("", "utbpanel-full-db-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(snapshot)
	uid := -1
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		uid = runner.UID(b.ID)
		if err := os.Chown(snapshot, uid, uid); err != nil {
			return err
		}
	}
	err = dbgate.Snapshot(ctx, filepath.Join(dir, "bot.db"), filepath.Join(snapshot, "bot.db"), uid)
	if err == nil {
		copy, err := os.OpenRoot(snapshot)
		if err != nil {
			return err
		}
		defer copy.Close()
		if err := a.RootFile(copy, "bot.db", path.Join(prefix, "bot.db"), false); err != nil {
			return err
		}
	} else if err != dbgate.ErrNoDB {
		return err
	}
	if files {
		if _, err := root.Lstat("files"); err == nil {
			if err := a.Tree(root, "files", path.Join(prefix, "files")); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (c *Core) MakeFullBackup(ctx context.Context, conn *wire.Conn, in FullBackupRequest) (backup.File, error) {
	if !c.backupMu.TryLock() {
		return backup.File{}, wire.Errorf("conflict", "бэкап уже создаётся")
	}
	defer c.backupMu.Unlock()
	c.backupBusy.Store(true)
	defer c.backupBusy.Store(false)
	cfg, err := c.fullSettings(ctx)
	if err != nil {
		return backup.File{}, err
	}
	includeFiles := cfg.Files
	if in.Files != nil {
		includeFiles = *in.Files
	}
	if conn == nil {
		return backup.File{}, wire.Errorf(wire.CodeUnavailable, "панель недоступна")
	}
	var part backup.PanelPart
	if err := conn.Call(ctx, "backups.panel.prepare", struct{}{}, &part); err != nil {
		return backup.File{}, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		conn.Call(cleanup, "backups.panel.release", backup.FileRef{ID: part.File.ID}, nil)
	}()
	if part.File.Size < 0 || part.File.BotID != 0 {
		return backup.File{}, fmt.Errorf("invalid panel snapshot")
	}
	reader := &backup.RemoteReader{Ctx: ctx, Ref: backup.FileRef{ID: part.File.ID}, Size: part.File.Size, ReadChunk: func(ctx context.Context, ref backup.FileRef) ([]byte, error) {
		var raw []byte
		err := conn.Call(ctx, "backups.panel.read", ref, &raw)
		return raw, err
	}}
	tmp, err := os.MkdirTemp("", "utbpanel-full-")
	if err != nil {
		return backup.File{}, err
	}
	defer os.RemoveAll(tmp)
	locked, unlock, err := c.ConfigGuard.Lock(ctx)
	if err != nil {
		return backup.File{}, err
	}
	key, err := os.ReadFile(secrets.MasterKeyPath(c.Cfg.KeysDir))
	if err != nil {
		unlock()
		return backup.File{}, err
	}
	master, err := secrets.ParseMasterKey(key)
	clear(key)
	if err != nil {
		unlock()
		return backup.File{}, err
	}
	defer clear(master)
	err = c.Users.Flush(locked)
	if err == nil {
		err = c.W.Now(locked, func(*sql.Tx) error { return nil })
	}
	if err == nil {
		err = dbgate.Snapshot(locked, c.DB.Path, filepath.Join(tmp, "core.db"), -1)
	}
	unlock()
	if err != nil {
		return backup.File{}, err
	}
	db, err := dbgate.OpenReadOnly(filepath.Join(tmp, "core.db"))
	if err != nil {
		return backup.File{}, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id,name,current_version,state FROM bots ORDER BY id`)
	if err != nil {
		return backup.File{}, err
	}
	bots := []backup.FullBot{}
	for rows.Next() {
		var bot backup.FullBot
		if err := rows.Scan(&bot.ID, &bot.Name, &bot.Version, &bot.State); err != nil {
			rows.Close()
			return backup.File{}, err
		}
		bots = append(bots, bot)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return backup.File{}, err
	}
	root, err := os.OpenRoot(tmp)
	if err != nil {
		return backup.File{}, err
	}
	defer root.Close()
	manifest := backup.FullManifest{Bots: bots, Versions: map[string]string{"core": buildinfo.String("core"), "panel": part.Version, "edge": buildinfo.String("edge"), "gate": buildinfo.String("gate")}}
	file, err := c.FullBackups.Create(ctx, 0, "tgpanel-"+time.Now().UTC().Format("20060102-150405")+".backup", func(out io.Writer) error {
		return backup.EncryptFull(ctx, out, master, manifest, func(a *backup.Archive) error {
			if err := a.RootFile(root, "core.db", "core.db", false); err != nil {
				return err
			}
			if err := addPanelBackup(ctx, a, reader); err != nil {
				return err
			}
			for _, bot := range bots {
				if err := c.addFullBot(ctx, a, bot, includeFiles); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		return backup.File{}, err
	}
	current, err := c.fullSettings(ctx)
	if err != nil {
		return file, err
	}
	if err := backup.Retain(c.FullBackups, current.Keep); err != nil {
		return file, err
	}
	c.putBackupValue(ctx, fullErrorKey, "")
	c.Ev.Emit("full_backup_created", 0, events.Info, map[string]any{"id": file.ID, "name": file.Name, "size": file.Size})
	return file, nil
}

func (c *Core) runFullBackups(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			cfg, err := c.fullSettings(ctx)
			if err != nil || !cfg.Enabled || c.backupBusy.Load() {
				continue
			}
			last, err := time.Parse(time.RFC3339Nano, c.backupValue(ctx, fullAutoKey))
			if err != nil {
				c.putBackupValue(ctx, fullAutoKey, now.UTC().Format(time.RFC3339Nano))
				continue
			}
			if cfg.Next(last).After(now) {
				continue
			}
			if err := c.putBackupValue(ctx, fullAutoKey, now.UTC().Format(time.RFC3339Nano)); err != nil {
				continue
			}
			var panel *wire.Conn
			for _, conn := range c.Srv.Conns() {
				if conn.Proto() == wire.ProtoCorePanel {
					panel = conn
					break
				}
			}
			if _, err := c.MakeFullBackup(ctx, panel, FullBackupRequest{Actor: journal.Actor{Source: "schedule", Name: "backup"}}); err != nil {
				c.putBackupValue(ctx, fullErrorKey, err.Error())
				c.Ev.Emit("full_backup_failed", 0, events.Error, map[string]any{"error": err.Error()})
			}
		}
	}
}

func (c *Core) registerFullBackups() {
	c.FullBackups = backup.FullFiles(filepath.Join(c.Cfg.DataDir, "backups", "full"))
	c.Mux.Handle("backups.status", wire.Func(func(ctx context.Context, _ *wire.Conn, _ struct{}) (backup.FullStatus, error) {
		return c.fullStatus(ctx)
	}))
	c.Mux.Handle("backups.settings", wire.Func(func(ctx context.Context, _ *wire.Conn, in backup.FullSettings) (struct{}, error) {
		return struct{}{}, c.setFullSettings(ctx, in)
	}))
	c.Mux.Handle("backups.make", wire.Func(func(ctx context.Context, conn *wire.Conn, in FullBackupRequest) (backup.File, error) {
		var file backup.File
		err := c.Journal.Do(ctx, in.Actor, "backup.make", 0, nil, func() error { var err error; file, err = c.MakeFullBackup(ctx, conn, in); return err })
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			c.putBackupValue(cleanup, fullErrorKey, err.Error())
		}
		return file, err
	}))
	c.Mux.Handle("backups.info", wire.Func(func(_ context.Context, _ *wire.Conn, ref backup.FileRef) (backup.File, error) {
		file, err := c.FullBackups.Info(ref)
		if err != nil {
			return file, wire.Errorf(wire.CodeNotFound, "%v", err)
		}
		return file, nil
	}))
	c.Mux.Handle("backups.read", wire.Func(func(_ context.Context, _ *wire.Conn, ref backup.FileRef) ([]byte, error) {
		return c.FullBackups.Read(ref)
	}))
	c.Mux.Handle("backups.downloaded", wire.Func(func(ctx context.Context, _ *wire.Conn, ref backup.FileRef) (struct{}, error) {
		if err := c.FullBackups.MarkDownloaded(ref); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, c.putBackupValue(ctx, fullDownloadKey, time.Now().UTC().Format(time.RFC3339Nano))
	}))
}
