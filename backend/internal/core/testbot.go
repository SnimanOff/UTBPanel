package core

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// A test bot is a separate Telegram bot attached to one bot of the panel
// only for test runs of its new versions (trial.go). It is an ordinary bot
// record with bots.test_for set: its own token, port, database and files,
// hidden from the bot list and deleted together with its bot. Test runs go
// only to it, so the variables and secrets of other bots are never replaced.

// TestBotReq attaches (Token set) or detaches a test bot.
type TestBotReq struct {
	BotID int64         `cbor:"1,keyasint"`
	Token string        `cbor:"2,keyasint,omitempty"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

// TestBotOf returns the test bot attached to botID, nil when none.
func (c *Core) TestBotOf(ctx context.Context, botID int64) (*Bot, error) {
	b, err := scanBot(c.DB.R.QueryRowContext(ctx, `SELECT `+botColumns+` FROM bots WHERE test_for = ? ORDER BY id LIMIT 1`, botID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// testBotName is a free panel name for the test bot of b.
func (c *Core) testBotName(ctx context.Context, name string) string {
	base := name
	if r := []rune(base); len(r) > 50 {
		base = string(r[:50])
	}
	base += " · тест"
	for i := 1; ; i++ {
		n := base
		if i > 1 {
			n += " " + strconv.Itoa(i)
		}
		var cnt int
		if c.DB.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM bots WHERE name = ?`, n).Scan(&cnt) == nil && cnt == 0 {
			return n
		}
	}
}

// AttachTestBot checks the token with getMe and makes that bot the test bot
// of in.BotID; an attached one gets the new token.
func (c *Core) AttachTestBot(ctx context.Context, in TestBotReq) (Bot, error) {
	b, err := c.Bot(ctx, in.BotID)
	if err != nil {
		return Bot{}, err
	}
	if b.TestFor != 0 {
		return Bot{}, wire.Errorf(wire.CodeBadRequest, "%s сам тестовый бот", b.Name)
	}
	if in.Token == "" {
		return Bot{}, wire.Errorf(wire.CodeBadRequest, "нужен токен тестового бота из @BotFather")
	}
	old, err := c.TestBotOf(ctx, in.BotID)
	if err != nil {
		return Bot{}, err
	}
	if old != nil {
		if c.Runner.IsRunning(old.ID) {
			if err := c.Runner.Stop(ctx, old.ID); err != nil {
				return Bot{}, err
			}
		}
		return c.SetToken(ctx, SetToken{BotID: old.ID, Token: in.Token, Actor: in.Actor})
	}
	tb, err := c.CreateBot(ctx, CreateBot{Name: c.testBotName(ctx, b.Name), Token: in.Token, TestFor: b.ID, Actor: in.Actor})
	var we *wire.Error
	if errors.As(err, &we) && we.Code == "exists" {
		return Bot{}, wire.Errorf("exists", "этот бот уже есть в панели: для тестов нужен отдельный бот — создайте его в @BotFather")
	}
	if err != nil {
		return Bot{}, err
	}
	c.Logs.Add(b.ID, "core", "привязан тестовый бот @"+tb.Username, nowUTC())
	return tb, nil
}

// DetachTestBot ends a test run on the test bot of botID and deletes the
// test bot with its data.
func (c *Core) DetachTestBot(ctx context.Context, botID int64) error {
	if err := c.botExists(ctx, botID); err != nil {
		return err
	}
	tb, err := c.TestBotOf(ctx, botID)
	if err != nil {
		return err
	}
	if tb == nil {
		return wire.Errorf(wire.CodeNotFound, "тестовый бот не привязан")
	}
	if t, _ := c.loadTrial(ctx, botID); t != nil {
		c.kvDelete(ctx, trialKey(botID))
	}
	if err := c.DeleteBot(ctx, tb.ID); err != nil {
		return err
	}
	c.Logs.Add(botID, "core", "тестовый бот @"+tb.Username+" отвязан и удалён", nowUTC())
	return nil
}

func (c *Core) registerTestBot() {
	c.Mux.Handle("testbot.attach", wire.Func(func(ctx context.Context, _ *wire.Conn, in TestBotReq) (b Bot, err error) {
		//nolint:gosec // the journal records that a token was set, not its value
		err = c.Journal.Do(ctx, in.Actor, "testbot.attach", in.BotID, map[string]string{"token": "задан"}, func() error {
			b, err = c.AttachTestBot(ctx, in)
			return err
		})
		return b, err
	}))
	c.Mux.Handle("testbot.detach", wire.Func(func(ctx context.Context, _ *wire.Conn, in TestBotReq) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "testbot.detach", in.BotID, nil, func() error {
			return c.DetachTestBot(ctx, in.BotID)
		})
	}))
}
