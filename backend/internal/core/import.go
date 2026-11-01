package core

import (
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
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

func copyImported(ctx context.Context, source *os.Root, name string, dest *os.Root, target string) error {
	if err := dest.MkdirAll(path.Dir(target), 0o750); err != nil {
		return err
	}
	in, err := source.Open(name)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := dest.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	buffer := make([]byte, backup.ChunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := in.Read(buffer)
		if n > 0 {
			if _, err := out.Write(buffer[:n]); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return out.Close()
		}
		if err != nil {
			return err
		}
	}
}
func integer(value any) (int64, error) {
	switch v := value.(type) {
	case json.Number:
		return v.Int64()
	case int64:
		return v, nil
	}
	return 0, fmt.Errorf("expected integer")
}

func restoreBotRows(ctx context.Context, tx *sql.Tx, root *os.Root, id int64) error {
	users, err := backup.NewTableRestorer(ctx, tx, "bot_users")
	if err != nil {
		return err
	}
	allow, err := backup.NewTableRestorer(ctx, tx, "bot_allow")
	if err != nil {
		return err
	}
	payments, err := backup.NewTableRestorer(ctx, tx, "payments")
	if err != nil {
		return err
	}
	f, err := root.Open("users.json")
	if err != nil {
		return err
	}
	defer f.Close()
	if err := backup.ReadRowsDocument(f, map[string]func(map[string]any) error{
		"users": func(row map[string]any) error {
			user, err := integer(row["user_id"])
			if err != nil {
				return err
			}
			premium, err := integer(row["is_premium"])
			if err != nil {
				return err
			}
			for _, key := range []string{"username", "language_code", "profile_updated_at"} {
				if _, ok := row[key].(string); !ok {
					return fmt.Errorf("invalid user profile")
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO tg_users(user_id,username,language_code,is_premium,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET username=excluded.username,language_code=excluded.language_code,is_premium=excluded.is_premium,updated_at=excluded.updated_at WHERE excluded.updated_at>tg_users.updated_at`, user, row["username"], row["language_code"], premium, row["profile_updated_at"]); err != nil {
				return err
			}
			for _, key := range []string{"username", "language_code", "is_premium", "profile_updated_at"} {
				delete(row, key)
			}
			row["bot_id"] = id
			return users.Insert(row, false)
		},
		"allow": func(row map[string]any) error { row["bot_id"] = id; return allow.Insert(row, false) },
	}); err != nil {
		return err
	}
	fp, err := root.Open("payments.json")
	if err != nil {
		return err
	}
	defer fp.Close()
	return backup.ReadRowsDocument(fp, map[string]func(map[string]any) error{"payments": func(row map[string]any) error {
		delete(row, "id")
		row["bot_id"] = id
		return payments.Insert(row, false)
	}})
}

// ImportBot restores under an allocated local ID. A failure removes only the
// new local rows/files: it must never delete the old server's Telegram webhook.
func (c *Core) ImportBot(ctx context.Context, in backup.ImportApply) (Bot, error) {
	ctx, unlock, err := c.ConfigGuard.Lock(ctx)
	if err != nil {
		return Bot{}, err
	}
	defer unlock()
	var result Bot
	err = c.Imports.With(in.ID, func(preview *backup.Preview, root *os.Root) error {
		if preview.BotID > 0 {
			var err error
			result, err = c.Bot(ctx, preview.BotID)
			return err
		}
		if err := validBotName(in.Name); err != nil {
			return err
		}
		wanted := map[string]bool{}
		for _, name := range preview.Document.Secrets {
			wanted[name] = true
			value, ok := in.Secrets[name]
			if !ok || (value.Kind != secrets.KindValue && value.Kind != secrets.KindFile) {
				return wire.Errorf(wire.CodeBadRequest, "задайте секрет %s", name)
			}
		}
		for name := range in.Secrets {
			if !wanted[name] {
				return wire.Errorf(wire.CodeBadRequest, "unexpected secret %s", name)
			}
		}
		me, err := c.TG.GetMe(ctx, strings.TrimSpace(in.Token))
		if err != nil {
			return telegramError(err)
		}
		if preview.Manifest.TelegramID != 0 && preview.Manifest.TelegramID != me.ID {
			return wire.Errorf(wire.CodeBadRequest, "токен принадлежит другому боту")
		}
		b, err := c.CreateBot(ctx, CreateBot{Name: in.Name, Token: in.Token, Actor: in.Actor})
		if err != nil {
			return err
		}
		success := false
		ownedDir := ""
		defer func() {
			if !success {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				c.DB.Tx(cleanup, func(tx *sql.Tx) error {
					for _, table := range []string{"bot_users", "bot_allow", "payments", "github_sources"} {
						if _, err := tx.ExecContext(cleanup, `DELETE FROM `+table+` WHERE bot_id=?`, b.ID); err != nil {
							return err
						}
					}
					_, err := tx.ExecContext(cleanup, `DELETE FROM bots WHERE id=?`, b.ID)
					return err
				})
				if ownedDir != "" {
					os.RemoveAll(ownedDir)
				}
			}
		}()
		botsDir := filepath.Join(c.Cfg.DataDir, "bots")
		if err := os.MkdirAll(botsDir, 0o711); err != nil {
			return err
		}
		stage, err := os.MkdirTemp(botsDir, ".import-")
		if err != nil {
			return err
		}
		ownedDir = stage
		dest, err := os.OpenRoot(stage)
		if err != nil {
			return err
		}
		copyErr := func() error {
			if err := dest.MkdirAll("files", 0o750); err != nil {
				return err
			}
			if err := dest.MkdirAll("releases", 0o750); err != nil {
				return err
			}
			for name := range preview.Manifest.Entries {
				target := ""
				switch {
				case name == "bot.db":
					target = name
				case strings.HasPrefix(name, "files/"):
					target = name
				case strings.HasPrefix(name, "bin/"):
					target = "releases/" + strings.TrimPrefix(name, "bin/")
				}
				if target != "" {
					if err := copyImported(ctx, root, name, dest, target); err != nil {
						return err
					}
				}
			}
			for _, release := range preview.Manifest.Releases {
				entry := preview.Manifest.Entries[path.Join("bin", release.Version, "bot")]
				release.SHA256 = entry.SHA256
				release.Size = entry.Size
				raw, err := json.Marshal(release)
				if err != nil {
					return err
				}
				meta := path.Join("releases", release.Version, "release.json")
				if err := dest.WriteFile(meta, raw, 0o640); err != nil {
					return err
				}
			}
			return nil
		}()
		dest.Close()
		if copyErr != nil {
			return copyErr
		}
		if err := filepath.WalkDir(stage, func(name string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			mode := os.FileMode(0o600)
			if entry.IsDir() {
				mode = 0o750
			} else if entry.Name() == "bot" && strings.Contains(name, string(filepath.Separator)+"releases"+string(filepath.Separator)) {
				mode = 0o500
			}
			if err := os.Chmod(name, mode); err != nil {
				return err
			}
			if runtime.GOOS == "linux" && os.Geteuid() == 0 {
				return os.Chown(name, runner.UID(b.ID), runner.UID(b.ID))
			}
			return nil
		}); err != nil {
			return err
		}
		finalDir := filepath.Join(botsDir, strconv.FormatInt(b.ID, 10))
		if _, err := os.Lstat(finalDir); !os.IsNotExist(err) {
			return fmt.Errorf("new bot directory already exists")
		}
		if err := os.Rename(stage, finalDir); err != nil {
			return err
		}
		ownedDir = finalDir
		settings := documentSettings(preview.Document)
		settings["pending_flags"] = preview.Document.Flags
		raw, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		if err := c.DB.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE bots SET settings=?,current_version=? WHERE id=?`, string(raw), preview.Manifest.CurrentVersion, b.ID); err != nil {
				return err
			}
			for name, value := range in.Secrets {
				if err := c.Sec.SetTx(ctx, tx, b.ID, name, value.Kind, value.Value); err != nil {
					return err
				}
			}
			g := preview.Document.GitHub
			if g.Repo != "" {
				if g.Asset == "" {
					g.Asset = "*linux_amd64*"
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO github_sources(bot_id,repo,asset_mask,auto_deploy) VALUES(?,?,?,?)`, b.ID, g.Repo, g.Asset, g.AutoDeploy); err != nil {
					return err
				}
				if err := c.Sec.SetTx(ctx, tx, b.ID, secrets.GitHubHookName, secrets.KindPanel, []byte(randomSecret())); err != nil {
					return err
				}
			}
			return restoreBotRows(ctx, tx, root, b.ID)
		}); err != nil {
			return err
		}
		if err := c.Bans.LoadBot(ctx, b.ID); err != nil {
			return err
		}
		c.loadPatterns(ctx, b.ID)
		preview.BotID = b.ID
		success = true
		result, err = c.Bot(ctx, b.ID)
		return err
	})
	return result, err
}

func (c *Core) registerImports() {
	c.Imports = &backup.Imports{Dir: filepath.Join(c.Cfg.DataDir, "imports")}
	c.Mux.Handle("imports.begin", wire.Func(func(context.Context, *wire.Conn, struct{}) (string, error) { return c.Imports.Begin() }))
	c.Mux.Handle("imports.preview", wire.Func(func(_ context.Context, _ *wire.Conn, id string) (backup.Preview, error) { return c.Imports.Preview(id) }))
	c.Mux.Handle("imports.chunk", wire.Func(func(_ context.Context, _ *wire.Conn, in backup.ImportChunk) (struct{}, error) {
		return struct{}{}, c.Imports.Append(in.ID, in.Offset, in.Data)
	}))
	c.Mux.Handle("imports.inspect", wire.Func(func(ctx context.Context, _ *wire.Conn, in backup.ImportInspect) (backup.Preview, error) {
		out, err := c.Imports.Inspect(ctx, in.ID, in.Password)
		if err != nil {
			return out, wire.Errorf(wire.CodeBadRequest, "%v", err)
		}
		return out, nil
	}))
	c.Mux.Handle("imports.apply", wire.Func(func(ctx context.Context, _ *wire.Conn, in backup.ImportApply) (Bot, error) {
		var out Bot
		err := c.Journal.Do(ctx, in.Actor, "bot.import", 0, nil, func() error { var err error; out, err = c.ImportBot(ctx, in); return err })
		return out, err
	}))
	c.Mux.Handle("imports.read", wire.Func(func(_ context.Context, _ *wire.Conn, ref backup.FileRef) ([]byte, error) { return c.Imports.Read(ref) }))
	c.Mux.Handle("imports.abort", wire.Func(func(_ context.Context, _ *wire.Conn, id string) (struct{}, error) {
		c.Imports.Abort(id)
		return struct{}{}, nil
	}))
}
