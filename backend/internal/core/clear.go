package core

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// ClearBot stops the bot and wipes what it accumulated: bot.db with its WAL,
// everything in its DATA_DIR (files/) and its logs. The bot itself stays as it
// is — token, settings, secrets, versions and the panel's history. A bot that
// was running is started again, so a test starts from a clean database.
//
// The canary shares the directory and the database, so it is stopped too.
func (c *Core) ClearBot(ctx context.Context, id int64) error {
	ctx, unlock, err := c.ConfigGuard.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := c.botExists(ctx, id); err != nil {
		return err
	}
	wasRunning := c.Runner.IsRunning(id)
	c.Runner.StopCanary(id)
	if err := c.Runner.Stop(ctx, id); err != nil {
		return err
	}
	dir := filepath.Join(c.Cfg.DataDir, "bots", strconv.FormatInt(id, 10))
	if err := clearDataDir(dir); err != nil {
		return err
	}
	// Unlink, never follow: the bot may leave a link in place of any of these.
	for _, name := range []string{"bot.db", "bot.db-wal", "bot.db-shm", "bot.db.restore"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := c.Logs.Remove(id); err != nil {
		return err
	}
	c.Logs.Add(id, "core", "database, files and logs cleared", nowUTC())
	if !wasRunning {
		return nil
	}
	// The data is already gone: a bot that cannot start now must not turn the
	// clear into an error.
	if err := c.StartBot(ctx, id); err != nil {
		c.Log.Warn("starting the bot after a clear failed", "bot", id, "err", err)
	}
	return nil
}

// clearDataDir keeps the DATA_DIR's owner and mode and confines removals to it.
func clearDataDir(dir string) error {
	bot, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = bot.Close() }()
	info, err := bot.Lstat("files")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("clear bot: DATA_DIR is not a directory")
	}
	files, err := bot.OpenRoot("files")
	if err != nil {
		return err
	}
	defer func() { _ = files.Close() }()
	opened, err := files.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return errors.New("clear bot: DATA_DIR changed while opening it")
	}
	entries, err := fs.ReadDir(files.FS(), ".")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := files.RemoveAll(e.Name()); err != nil {
			return err
		}
	}
	return nil
}
