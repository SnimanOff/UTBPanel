package core

import (
	"context"
	"slices"

	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/sdkhost"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

type TaskRequest struct {
	BotID int64         `cbor:"1,keyasint"`
	Name  string        `cbor:"2,keyasint"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

func (c *Core) registerSDKTasks() {
	c.SDKTasks = sdkhost.NewTasks(c.DB, c.SDK, func(id int64) bool {
		info := c.SDK.Info(id, "")
		if info == nil || !info.Connected || !slices.Contains(info.Modules, "tasks") {
			return false
		}
		if !c.Runner.IsRunning(id) {
			return false
		}
		var state string
		if err := c.DB.R.QueryRow(`SELECT state FROM bots WHERE id=?`, id).Scan(&state); err != nil || state != runner.Running {
			return false
		}
		uses, ready := c.SDK.Readiness(id, "")
		return !uses || ready
	})
	c.SDKTasks.Redact = c.Sec.Redact.Redact
	c.OnClose(func(context.Context) error { c.SDKTasks.Close(); return nil })
	c.onStopping = append(c.onStopping, func(_ context.Context, b runner.Bot) { c.SDKTasks.SuspendBot(b.ID) })
	c.onStarted = append(c.onStarted, func(_ context.Context, b runner.Bot) { c.SDKTasks.ResumeBot(b.ID) })
	c.Mux.Handle("sdk.tasks.list", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (sdkhost.TasksView, error) {
		if err := c.botExists(ctx, id); err != nil {
			return sdkhost.TasksView{}, err
		}
		return c.SDKTasks.List(ctx, id)
	}))
	c.Mux.Handle("sdk.tasks.run", wire.Func(func(ctx context.Context, _ *wire.Conn, in TaskRequest) (int64, error) {
		if err := c.botExists(ctx, in.BotID); err != nil {
			return 0, err
		}
		var runID int64
		err := c.Journal.Do(ctx, in.Actor, "sdk.tasks.run", in.BotID, map[string]string{"name": in.Name}, func() error {
			var err error
			runID, err = c.SDKTasks.Start(ctx, in.BotID, in.Name, "manual")
			return err
		})
		return runID, err
	}))
}
