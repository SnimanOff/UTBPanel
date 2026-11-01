package core

import (
	"context"

	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/users"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// ClosedReq changes the closed mode or its list.
type ClosedReq struct {
	BotID   int64         `cbor:"1,keyasint"`
	Enabled bool          `cbor:"2,keyasint,omitempty"`
	Text    string        `cbor:"3,keyasint,omitempty"`
	User    string        `cbor:"4,keyasint,omitempty"` // id or @username
	Note    string        `cbor:"5,keyasint,omitempty"`
	UserID  int64         `cbor:"6,keyasint,omitempty"`
	Actor   journal.Actor `cbor:"9,keyasint"`
}

func (c *Core) registerClosed() {
	c.Mux.Handle("closed.get", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (users.ClosedState, error) {
		return c.Closed.State(ctx, id)
	}))
	c.Mux.Handle("closed.set", wire.Func(func(ctx context.Context, _ *wire.Conn, in ClosedReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "closed.set", in.BotID, map[string]any{"enabled": in.Enabled, "text": in.Text}, func() error {
			if err := c.botExists(ctx, in.BotID); err != nil {
				return err
			}
			return c.Closed.Set(ctx, in.BotID, in.Enabled, in.Text)
		})
	}))
	c.Mux.Handle("closed.allow", wire.Func(func(ctx context.Context, _ *wire.Conn, in ClosedReq) (int64, error) {
		var id int64
		err := c.Journal.Do(ctx, in.Actor, "closed.allow", in.BotID, map[string]any{"user": in.User}, func() error {
			var err error
			if id, err = c.Closed.Resolve(ctx, in.BotID, in.User); err != nil {
				return wire.Errorf(wire.CodeNotFound, "нет такого пользователя: @username работает только для тех, кто уже писал боту")
			}
			return c.Closed.Allow(ctx, in.BotID, id, in.Note)
		})
		return id, err
	}))
	c.Mux.Handle("closed.remove", wire.Func(func(ctx context.Context, _ *wire.Conn, in ClosedReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "closed.remove", in.BotID, map[string]any{"user": in.UserID}, func() error {
			return c.Closed.Remove(ctx, in.BotID, in.UserID)
		})
	}))
}
