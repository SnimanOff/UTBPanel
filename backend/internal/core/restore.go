package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

const restoreWaitEdgeKey = "restore_wait_edge"

type restorePlan struct {
	ID        string
	Directory string
	Manifest  backup.FullManifest
	PanelID   string
	Panel     *wire.Conn
}

func (c *Core) cleanForRestore(ctx context.Context) error {
	has, err := HasBots(ctx, c.DB.R)
	if err != nil {
		return err
	}
	if has {
		return wire.Errorf("conflict", "восстановление возможно только на чистой установке без ботов")
	}
	return nil
}
func (c *Core) PrepareRestore(ctx context.Context, id string) (backup.RestoreResult, error) {
	c.restoreMu.Lock()
	defer c.restoreMu.Unlock()
	ctx, unlock, err := c.ConfigGuard.Lock(ctx)
	if err != nil {
		return backup.RestoreResult{}, err
	}
	defer unlock()
	if c.restoring.Load() {
		return backup.RestoreResult{}, wire.Errorf("conflict", "восстановление уже запущено")
	}
	if err := c.cleanForRestore(ctx); err != nil {
		return backup.RestoreResult{}, err
	}
	var panel *wire.Conn
	for _, conn := range c.Srv.Conns() {
		if conn.Proto() == wire.ProtoCorePanel {
			panel = conn
			break
		}
	}
	if panel == nil {
		return backup.RestoreResult{}, wire.Errorf(wire.CodeUnavailable, "панель недоступна")
	}
	keyText, err := os.ReadFile(secrets.MasterKeyPath(c.Cfg.KeysDir))
	if err != nil {
		return backup.RestoreResult{}, err
	}
	key, err := secrets.ParseMasterKey(keyText)
	clear(keyText)
	if err != nil {
		return backup.RestoreResult{}, err
	}
	defer clear(key)
	var plan restorePlan
	err = c.restoreUploads.With(id, func(dir string, root *os.Root) error {
		os.RemoveAll(filepath.Join(dir, "data"))
		if err := root.Mkdir("data", 0o700); err != nil {
			return err
		}
		file, err := root.Open("upload.bin")
		if err != nil {
			return err
		}
		defer file.Close()
		manifest, err := backup.ExtractFull(ctx, file, key, filepath.Join(dir, "data"))
		if err != nil {
			return err
		}
		for _, name := range []string{"core.db", "panel.db"} {
			var db *store.DB
			if name == "core.db" {
				db, err = store.Open(ctx, filepath.Join(dir, "data", name), migrations.Core())
			} else {
				db, err = store.Open(ctx, filepath.Join(dir, "data", name), migrations.Panel())
			}
			if err != nil {
				return err
			}
			if name == "core.db" {
				box, err := secrets.NewBox(key)
				if err != nil {
					db.Close()
					return err
				}
				rows, err := db.R.QueryContext(ctx, `SELECT bot_id,name,ciphertext,nonce FROM secrets`)
				if err != nil {
					db.Close()
					return err
				}
				for rows.Next() {
					var bot int64
					var name string
					var cipher, nonce []byte
					if err := rows.Scan(&bot, &name, &cipher, &nonce); err != nil {
						rows.Close()
						db.Close()
						return err
					}
					plain, err := box.Open(bot, name, cipher, nonce)
					clear(plain)
					if err != nil {
						rows.Close()
						db.Close()
						return err
					}
				}
				err = rows.Err()
				rows.Close()
				if err != nil {
					db.Close()
					return err
				}
				err = db.Tx(ctx, func(tx *sql.Tx) error {
					for _, query := range []string{`DELETE FROM canaries`, `UPDATE bots SET previous_version=''`} {
						if _, err := tx.ExecContext(ctx, query); err != nil {
							return err
						}
					}
					if _, err := tx.ExecContext(ctx, `DELETE FROM kv WHERE key=?`, kvEdgeCert); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `INSERT INTO kv(key,value) VALUES(?, '1') ON CONFLICT(key) DO UPDATE SET value='1'`, restoreWaitEdgeKey)
					return err
				})
				if err != nil {
					db.Close()
					return err
				}
			}
			if err := db.Close(); err != nil {
				return err
			}
		}
		if err := root.MkdirAll("data/bots", 0o711); err != nil {
			return err
		}
		plan = restorePlan{ID: id, Directory: filepath.Join(dir, "data"), Manifest: manifest, Panel: panel}
		return nil
	})
	if err != nil {
		return backup.RestoreResult{}, err
	}
	var panelID string
	if err := panel.Call(ctx, "restore.panel.begin", struct{}{}, &panelID); err != nil {
		return backup.RestoreResult{}, err
	}
	prepared := false
	defer func() {
		if !prepared {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			panel.Call(cleanup, "restore.panel.abort", panelID, nil)
		}
	}()
	partWriter := &restorePartWriter{ctx: ctx, conn: panel, id: panelID}
	root, err := os.OpenRoot(plan.Directory)
	if err != nil {
		return backup.RestoreResult{}, err
	}
	err = backup.Parts(ctx, partWriter, func(a *backup.Archive) error {
		if err := a.RootFile(root, "panel.db", "panel.db", false); err != nil {
			return err
		}
		return a.Tree(root, "pki", "pki")
	})
	root.Close()
	if err != nil {
		return backup.RestoreResult{}, err
	}
	if err := panel.Call(ctx, "restore.panel.prepare", panelID, nil); err != nil {
		return backup.RestoreResult{}, err
	}
	prepared = true
	plan.PanelID = panelID
	c.restorePlan = &plan
	return backup.RestoreResult{ID: id, Bots: len(plan.Manifest.Bots)}, nil
}

type restorePartWriter struct {
	ctx    context.Context
	conn   *wire.Conn
	id     string
	offset int64
}

func (w *restorePartWriter) Write(data []byte) (int, error) {
	count := 0
	for len(data) > 0 {
		size := min(len(data), backup.ChunkSize)
		if err := w.conn.Call(w.ctx, "restore.panel.chunk", backup.ImportChunk{ID: w.id, Offset: w.offset, Data: data[:size]}, nil); err != nil {
			return count, err
		}
		count += size
		w.offset += int64(size)
		data = data[size:]
	}
	return count, nil
}

func (c *Core) commitRestore(ctx context.Context, id string) (wire.AfterReply, error) {
	c.restoreMu.Lock()
	defer c.restoreMu.Unlock()
	ctx, unlock, err := c.ConfigGuard.Lock(ctx)
	if err != nil {
		return wire.AfterReply{}, err
	}
	defer unlock()
	if err := c.cleanForRestore(ctx); err != nil {
		return wire.AfterReply{}, err
	}
	if c.restorePlan == nil || c.restorePlan.ID != id {
		return wire.AfterReply{}, wire.Errorf("conflict", "архив ещё не подготовлен")
	}
	if !c.restoring.CompareAndSwap(false, true) {
		return wire.AfterReply{}, wire.Errorf("conflict", "восстановление уже запущено")
	}
	plan := *c.restorePlan
	if err := restoreOwnership(filepath.Join(plan.Directory, "bots")); err != nil {
		c.restoring.Store(false)
		return wire.AfterReply{}, err
	}
	apply := func() error {
		result := backup.RestoreResult{ID: id, Bots: len(plan.Manifest.Bots), Done: true}
		err := backup.InstallThen(c.Cfg.DataDir, []backup.InstallItem{{Source: filepath.Join(plan.Directory, "core.db"), Target: filepath.Join(c.Cfg.DataDir, "core.db")}, {Source: filepath.Join(plan.Directory, "bots"), Target: filepath.Join(c.Cfg.DataDir, "bots")}}, func() error {
			timeout, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var reply backup.RestoreResult
			if err := plan.Panel.Call(timeout, "restore.panel.commit", plan.PanelID, &reply); err != nil {
				return err
			}
			if reply.Error != "" {
				return fmt.Errorf("panel restore: %s", reply.Error)
			}
			return nil
		})
		if err != nil {
			result.Error = err.Error()
		}
		raw, _ := json.Marshal(result)
		writeErr := os.WriteFile(filepath.Join(c.Cfg.DataDir, "restore-result.json"), raw, 0o600)
		if err == nil && writeErr != nil {
			err = writeErr
		}
		if err == nil {
			c.restoreUploads.Delete(id)
		}
		return err
	}
	return wire.AfterReply{Value: backup.RestoreResult{ID: id, Bots: len(plan.Manifest.Bots)}, Run: func() { c.restart <- app.RestartRequest{Apply: apply} }}, nil
}
func restoreOwnership(dir string) error {
	return filepath.WalkDir(dir, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, name)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.Chmod(name, 0o711)
		}
		parts := strings.Split(relative, string(filepath.Separator))
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("invalid restored bot directory")
		}
		mode := os.FileMode(0o600)
		if entry.IsDir() {
			mode = 0o750
		} else if entry.Name() == "bot" && len(parts) == 4 && parts[1] == "releases" {
			mode = 0o500
		}
		if err := os.Chmod(name, mode); err != nil {
			return err
		}
		if runtime.GOOS == "linux" && os.Geteuid() == 0 {
			return os.Chown(name, runner.UID(id), runner.UID(id))
		}
		return nil
	})
}

func (c *Core) resumeRestoredBots() {
	if c.runCtx == nil || !c.restoreStartMu.TryLock() {
		return
	}
	defer c.restoreStartMu.Unlock()
	ctx := c.runCtx
	if c.backupValue(ctx, restoreWaitEdgeKey) == "" {
		return
	}
	c.PushEdge(ctx)
	c.Runner.Restore(ctx)
	if ctx.Err() == nil {
		c.DB.W.ExecContext(ctx, `DELETE FROM kv WHERE key=?`, restoreWaitEdgeKey)
	}
}

func (c *Core) registerRestore() {
	c.restoreUploads = &backup.Staging{Dir: filepath.Join(c.Cfg.DataDir, "restore")}
	onlyCLI := func(conn *wire.Conn) error {
		if conn == nil || conn.Proto() != wire.ProtoCoreCLI || !localCLI(conn) {
			return wire.Errorf("forbidden", "восстановление доступно только через core backup restore по SSH")
		}
		return nil
	}
	c.Mux.Handle("restore.begin", wire.Func(func(ctx context.Context, conn *wire.Conn, _ struct{}) (string, error) {
		if err := onlyCLI(conn); err != nil {
			return "", err
		}
		if err := c.cleanForRestore(ctx); err != nil {
			return "", err
		}
		return c.restoreUploads.Begin()
	}))
	c.Mux.Handle("restore.chunk", wire.Func(func(_ context.Context, conn *wire.Conn, in backup.ImportChunk) (struct{}, error) {
		if err := onlyCLI(conn); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, c.restoreUploads.Append(in.ID, in.Offset, in.Data)
	}))
	c.Mux.Handle("restore.prepare", wire.Func(func(ctx context.Context, conn *wire.Conn, id string) (backup.RestoreResult, error) {
		if err := onlyCLI(conn); err != nil {
			return backup.RestoreResult{}, err
		}
		return c.PrepareRestore(ctx, id)
	}))
	c.Mux.Handle("restore.commit", wire.Func(func(ctx context.Context, conn *wire.Conn, id string) (wire.AfterReply, error) {
		if err := onlyCLI(conn); err != nil {
			return wire.AfterReply{}, err
		}
		return c.commitRestore(ctx, id)
	}))
	c.Mux.Handle("restore.abort", wire.Func(func(_ context.Context, conn *wire.Conn, id string) (struct{}, error) {
		if err := onlyCLI(conn); err != nil {
			return struct{}{}, err
		}
		if !c.restoring.Load() {
			c.restoreUploads.Delete(id)
		}
		return struct{}{}, nil
	}))
	c.Mux.Handle("restore.status", wire.Func(func(_ context.Context, conn *wire.Conn, id string) (backup.RestoreResult, error) {
		if err := onlyCLI(conn); err != nil {
			return backup.RestoreResult{}, err
		}
		raw, err := os.ReadFile(filepath.Join(c.Cfg.DataDir, "restore-result.json"))
		if err != nil {
			return backup.RestoreResult{}, err
		}
		var result backup.RestoreResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return result, err
		}
		if result.ID != id {
			return result, wire.Errorf(wire.CodeNotFound, "нет результата этого восстановления")
		}
		return result, nil
	}))
}
