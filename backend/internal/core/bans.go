package core

import (
	"context"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/users"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// BanReq bans or unbans a user. BotID 0 with All means every bot.
type BanReq struct {
	BotID  int64         `cbor:"1,keyasint"`
	UserID int64         `cbor:"2,keyasint"`
	Reason string        `cbor:"3,keyasint,omitempty"`
	Hours  int           `cbor:"4,keyasint,omitempty"` // 0 = forever
	All    bool          `cbor:"5,keyasint,omitempty"`
	Actor  journal.Actor `cbor:"9,keyasint"`
}

func (c *Core) registerBans() {
	c.Mux.Handle("users.ban", wire.Func(func(ctx context.Context, _ *wire.Conn, in BanReq) (struct{}, error) {
		cmd := "users.ban"
		if in.All {
			cmd = "users.ban_all"
		}
		return struct{}{}, c.Journal.Do(ctx, in.Actor, cmd, in.BotID, map[string]any{"user": in.UserID, "reason": in.Reason, "hours": in.Hours}, func() error {
			ids := []int64{in.BotID}
			if in.All {
				ids = ids[:0]
				bots, _ := c.Bots(ctx)
				for _, b := range bots {
					ids = append(ids, b.ID)
				}
			} else if err := c.botExists(ctx, in.BotID); err != nil {
				return err
			}
			by := in.Actor.Name
			if by == "" {
				by = in.Actor.Source
			}
			for _, id := range ids {
				if err := c.Bans.Ban(ctx, id, in.UserID, in.Reason, time.Duration(in.Hours)*time.Hour, by); err != nil {
					if err == users.ErrReason {
						return wire.Errorf(wire.CodeBadRequest, "укажите причину бана")
					}
					return err
				}
			}
			return nil
		})
	}))
	c.Mux.Handle("users.unban", wire.Func(func(ctx context.Context, _ *wire.Conn, in BanReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "users.unban", in.BotID, map[string]any{"user": in.UserID}, func() error {
			return c.Bans.Unban(ctx, in.BotID, in.UserID)
		})
	}))
}
