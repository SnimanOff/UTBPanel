package core

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// BotRef names a bot in life cycle requests.
type BotRef struct {
	BotID int64 `cbor:"1,keyasint"`
}

// Rename changes the panel name of a bot.
type Rename struct {
	BotID int64  `cbor:"1,keyasint"`
	Name  string `cbor:"2,keyasint"`
}

// WebhookURL is the public webhook address of a bot.
func (c *Core) WebhookURL(id int64) string {
	host := net.JoinHostPort(c.Cfg.PublicAddr, strconv.Itoa(c.Cfg.WebhookPort))
	return "https://" + host + "/hook/" + strconv.FormatInt(id, 10)
}

func (c *Core) newRunner() *runner.Runner {
	self, _ := os.Executable()
	l := c.Cfg.Limits
	return runner.New(runner.Config{
		DataDir:        c.Cfg.DataDir,
		WebhookURL:     c.WebhookURL,
		Secrets:        c.Sec,
		DB:             c.DB,
		Log:            c.Log,
		DropPrivileges: runtime.GOOS == "linux" && os.Geteuid() == 0,
		ExecSelf:       self,
		DefaultLimits: runner.Limits{
			Memory: int64(l.Memory), File: int64(l.File), FilesTotal: int64(l.FilesTotal),
			OpenFiles: int64(l.OpenFiles), Threads: int64(l.Threads),
		},
		Hooks: runner.Hooks{
			Output: func(l runner.Line) { c.Logs.Add(l.BotID, l.Stream, l.Text, l.Time) },
			Event: func(id int64, typ, sev string, data map[string]any) {
				c.Ev.Emit(typ, id, sev, data)
			},
		},
	})
}

func runnerError(err error) error {
	switch {
	case errors.Is(err, runner.ErrNoVersion):
		return wire.Errorf("no_version", "upload a version first")
	case errors.Is(err, runner.ErrRunning):
		return wire.Errorf("conflict", "the bot is already running")
	case err != nil:
		return wire.Errorf("start_failed", "%v", err)
	}
	return nil
}

// StartBot starts a bot by id.
func (c *Core) StartBot(ctx context.Context, id int64) error {
	b, err := runner.LoadBot(ctx, c.DB, id)
	if err != nil {
		return errNoBot
	}
	return runnerError(c.Runner.Start(ctx, b))
}

// StopBot stops a bot.
func (c *Core) StopBot(ctx context.Context, id int64) error {
	if err := c.botExists(ctx, id); err != nil {
		return err
	}
	return c.Runner.Stop(ctx, id)
}

// RestartBot is stop + start.
func (c *Core) RestartBot(ctx context.Context, id int64) error {
	if err := c.StopBot(ctx, id); err != nil {
		return err
	}
	return c.StartBot(ctx, id)
}

// RenameBot changes the panel name; the Telegram name is in the profile.
func (c *Core) RenameBot(ctx context.Context, in Rename) error {
	if err := validBotName(in.Name); err != nil {
		return err
	}
	res, err := c.DB.W.ExecContext(ctx, `UPDATE bots SET name = ? WHERE id = ?`, in.Name, in.BotID)
	if err != nil {
		return wire.Errorf("exists", "a bot named %q already exists", in.Name)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNoBot
	}
	return nil
}

func (c *Core) registerLifecycle() {
	op := func(name string, fn func(context.Context, int64) error) {
		c.Mux.Handle(name, wire.Func(func(ctx context.Context, _ *wire.Conn, in BotRef) (struct{}, error) {
			return struct{}{}, fn(ctx, in.BotID)
		}))
	}
	op("bots.start", c.StartBot)
	op("bots.stop", c.StopBot)
	op("bots.restart", c.RestartBot)
	c.Mux.Handle("bots.rename", wire.Func(func(ctx context.Context, _ *wire.Conn, in Rename) (struct{}, error) {
		return struct{}{}, c.RenameBot(ctx, in)
	}))
}
