package core

import (
	"context"
	"encoding/json"

	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/metrics"
	"github.com/SnimanOff/UTBPanel/backend/internal/sdkhost"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
	"github.com/SnimanOff/UTBPanel/backend/sdk/flags"
)

type FlagRequest struct {
	BotID int64         `cbor:"1,keyasint"`
	Name  string        `cbor:"2,keyasint"`
	Value flags.Value   `cbor:"3,keyasint"`
	Reset bool          `cbor:"4,keyasint"`
	Actor journal.Actor `cbor:"9,keyasint"`
}
type FlagMarksRequest = metrics.FlagMarksRequest

func (c *Core) registerSDKFlags(ctx context.Context) error {
	if _, err := c.DB.W.ExecContext(ctx, `UPDATE sdk_flags SET main_active=0,canary_active=0`); err != nil {
		return err
	}
	c.SDKFlags = &sdkhost.Flags{DB: c.DB, Lock: c.ConfigGuard.Lock, Changed: func(id int64, name string) {
		c.Ev.Emit("sdk_flag_changed", id, events.Info, map[string]any{"name": name})
	}}
	c.SDKFlags.Restore = func(ctx context.Context, id int64) (map[string]flags.Value, error) {
		m, err := c.Settings(ctx, id)
		if err != nil {
			return nil, err
		}
		out := map[string]flags.Value{}
		if raw := m["pending_flags"]; len(raw) > 0 {
			err = json.Unmarshal(raw, &out)
		}
		return out, err
	}
	c.SDKFlags.Consumed = func(ctx context.Context, id int64, names []string) error {
		m, err := c.SDKFlags.Restore(ctx, id)
		if err != nil {
			return err
		}
		for _, name := range names {
			delete(m, name)
		}
		return c.SetSetting(ctx, id, "pending_flags", m)
	}
	c.Mux.Handle("sdk.flags.list", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) ([]sdkhost.Flag, error) {
		if err := c.botExists(ctx, id); err != nil {
			return nil, err
		}
		return c.SDKFlags.List(ctx, id)
	}))
	c.Mux.Handle("sdk.flags.set", wire.Func(func(ctx context.Context, _ *wire.Conn, in FlagRequest) (any, error) {
		if err := c.botExists(ctx, in.BotID); err != nil {
			return nil, err
		}
		args := map[string]any{"name": in.Name, "reset": in.Reset}
		if !in.Reset {
			args["value"] = in.Value
		}
		return nil, c.Journal.Do(ctx, in.Actor, "flag.set", in.BotID, args, func() error { return c.SDKFlags.Set(ctx, in.BotID, in.Name, in.Value, in.Reset) })
	}))
	c.Mux.Handle("sdk.flags.marks", wire.Func(func(ctx context.Context, _ *wire.Conn, in FlagMarksRequest) ([]sdkhost.FlagMark, error) {
		return c.SDKFlags.Marks(ctx, in.BotID, in.From)
	}))
	return nil
}
