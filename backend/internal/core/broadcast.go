package core

import (
	"context"
	"errors"

	"github.com/SnimanOff/UTBPanel/backend/internal/broadcast"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// BroadcastReq is a broadcast operation; TestChat is the owner's Telegram
// ID for "Отправить мне".
type BroadcastReq struct {
	B        broadcast.B   `cbor:"1,keyasint"`
	ID       int64         `cbor:"2,keyasint,omitempty"`
	TestChat int64         `cbor:"3,keyasint,omitempty"`
	Limit    int           `cbor:"4,keyasint,omitempty"`
	Actor    journal.Actor `cbor:"9,keyasint"`
}

func (c *Core) newBroadcasts() *broadcast.Service {
	return &broadcast.Service{
		DB:    c.DB,
		TG:    c.TG,
		Limit: broadcast.NewLimiter(),
		Token: c.Token,
		Log:   c.Log,
		Event: func(botID int64, typ string, data map[string]any) { c.Ev.Emit(typ, botID, events.Info, data) },
	}
}

func broadcastError(err error) error {
	switch {
	case errors.Is(err, broadcast.ErrEmpty):
		return wire.Errorf(wire.CodeBadRequest, "нужен текст или картинка")
	case errors.Is(err, broadcast.ErrDeleteAfter):
		return wire.Errorf(wire.CodeBadRequest, "автоудаление: от 1 до 47 часов — Telegram даёт удалять сообщения только 48 часов")
	case errors.Is(err, broadcast.ErrNotFound):
		return wire.Errorf(wire.CodeNotFound, "нет такой рассылки")
	case errors.Is(err, broadcast.ErrTooLate):
		return wire.Errorf("conflict", "прошло больше 48 часов: Telegram уже не даёт менять эти сообщения")
	case errors.Is(err, broadcast.ErrState):
		return wire.Errorf("conflict", "в этом состоянии нельзя")
	}
	var we *wire.Error
	if errors.As(err, &we) {
		return err
	}
	if _, ok := telegram.AsError(err); ok {
		return telegramError(err)
	}
	return err
}

func (c *Core) registerBroadcasts() {
	// Early warning before scheduled maintenance: users active this week.
	c.warnHook = func(ctx context.Context, botID int64, text string) error {
		_, err := c.Bcast.Create(ctx, broadcast.B{BotID: botID, Text: text, ParseMode: "HTML", Audience: "active", ActiveDays: 7,
			Kind: "maintenance_warning", CreatedBy: "schedule"})
		return err
	}
	c.Mux.Handle("broadcast.create", wire.Func(func(ctx context.Context, _ *wire.Conn, in BroadcastReq) (broadcast.B, error) {
		var out broadcast.B
		err := c.Journal.Do(ctx, in.Actor, "broadcast.create", in.B.BotID, map[string]any{"audience": in.B.Audience, "starts_at": in.B.StartsAt, "delete_after": in.B.DeleteAfter}, func() error {
			if err := c.botExists(ctx, in.B.BotID); err != nil {
				return err
			}
			in.B.CreatedBy = in.Actor.Name
			b, err := c.Bcast.Create(ctx, in.B)
			out = b
			return err
		})
		if err != nil {
			return out, broadcastError(err)
		}
		out.Photo = nil
		return out, nil
	}))
	c.Mux.Handle("broadcast.test", wire.Func(func(ctx context.Context, _ *wire.Conn, in BroadcastReq) (struct{}, error) {
		if in.TestChat == 0 {
			return struct{}{}, wire.Errorf(wire.CodeBadRequest, "укажите свой Telegram ID в настройках панели")
		}
		if in.B.Text == "" && len(in.B.Photo) == 0 {
			return struct{}{}, broadcastError(broadcast.ErrEmpty)
		}
		return struct{}{}, broadcastErrorOrNil(c.Bcast.SendTest(ctx, in.B, in.TestChat))
	}))
	c.Mux.Handle("broadcast.list", wire.Func(func(ctx context.Context, _ *wire.Conn, in BroadcastReq) ([]broadcast.B, error) {
		return c.Bcast.List(ctx, in.B.BotID, in.Limit)
	}))
	c.Mux.Handle("broadcast.get", wire.Func(func(ctx context.Context, _ *wire.Conn, in BroadcastReq) (broadcast.B, error) {
		b, err := c.Bcast.Get(ctx, in.ID)
		if err == nil && b.BotID != in.B.BotID {
			err = broadcast.ErrNotFound
		}
		b.Photo = nil
		return b, broadcastErrorOrNil(err)
	}))
	c.Mux.Handle("broadcast.estimate", wire.Func(func(ctx context.Context, _ *wire.Conn, in BroadcastReq) (int, error) {
		return c.Bcast.Estimate(ctx, in.B.BotID, in.B.Audience, in.B.ActiveDays)
	}))
	owned := func(ctx context.Context, in BroadcastReq) error {
		b, err := c.Bcast.Get(ctx, in.ID)
		if err == nil && b.BotID != in.B.BotID {
			err = broadcast.ErrNotFound
		}
		return err
	}
	c.Mux.Handle("broadcast.edit", wire.Func(func(ctx context.Context, _ *wire.Conn, in BroadcastReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "broadcast.edit", in.B.BotID, map[string]int64{"id": in.ID}, func() error {
			if err := owned(ctx, in); err != nil {
				return broadcastError(err)
			}
			return broadcastErrorOrNil(c.Bcast.Edit(ctx, in.ID, in.B.Text, in.B.Buttons))
		})
	}))
	c.Mux.Handle("broadcast.recall", wire.Func(func(ctx context.Context, _ *wire.Conn, in BroadcastReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "broadcast.recall", in.B.BotID, map[string]int64{"id": in.ID}, func() error {
			if err := owned(ctx, in); err != nil {
				return broadcastError(err)
			}
			return broadcastErrorOrNil(c.Bcast.Recall(ctx, in.ID))
		})
	}))
	for op, fn := range map[string]func(context.Context, int64) error{
		"broadcast.pause":  c.Bcast.Pause,
		"broadcast.resume": c.Bcast.Resume,
		"broadcast.cancel": c.Bcast.Cancel,
	} {
		c.Mux.Handle(op, wire.Func(func(ctx context.Context, _ *wire.Conn, in BroadcastReq) (struct{}, error) {
			return struct{}{}, c.Journal.Do(ctx, in.Actor, op, in.B.BotID, map[string]int64{"id": in.ID}, func() error {
				b, err := c.Bcast.Get(ctx, in.ID)
				if err == nil && b.BotID != in.B.BotID {
					err = broadcast.ErrNotFound
				}
				if err == nil {
					err = fn(ctx, in.ID)
				}
				return broadcastErrorOrNil(err)
			})
		}))
	}
}

func broadcastErrorOrNil(err error) error {
	if err == nil {
		return nil
	}
	return broadcastError(err)
}
