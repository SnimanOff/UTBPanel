package core

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Bot states.
const (
	StateRunning = "running"
	StateStopped = "stopped"
	StatePaused  = "paused"
	StateCrashed = "crashed"
	StateError   = "error"
)

// Bot is one bot as the core knows it.
type Bot struct {
	ID        int64     `cbor:"1,keyasint" json:"id"`
	Name      string    `cbor:"2,keyasint" json:"name"`
	Username  string    `cbor:"3,keyasint" json:"username"`
	TgID      int64     `cbor:"4,keyasint" json:"tg_id"`
	State     string    `cbor:"5,keyasint" json:"state"`
	Port      int       `cbor:"6,keyasint" json:"port"`
	Version   string    `cbor:"7,keyasint" json:"version"`
	CreatedAt time.Time `cbor:"8,keyasint" json:"created_at"`
}

// CreateBot is the request to add a bot.
type CreateBot struct {
	Name  string        `cbor:"1,keyasint"`
	Token string        `cbor:"2,keyasint"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

// SetToken replaces a bot token.
type SetToken struct {
	BotID int64         `cbor:"1,keyasint"`
	Token string        `cbor:"2,keyasint"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

// CodeTelegram marks errors answered by Telegram; Data holds its answer.
const CodeTelegram = "telegram"

func telegramError(err error) error {
	if te, ok := telegram.AsError(err); ok {
		return &wire.Error{Code: CodeTelegram, Message: te.Description, Data: wire.Raw(mustCBOR(te.Raw))}
	}
	return wire.Errorf(CodeTelegram, "%s", telegram.MaskToken(err.Error()))
}

func mustCBOR(raw []byte) []byte {
	b, _ := wire.Marshal(string(raw))
	return b
}

const botColumns = `id, name, username, tg_id, state, port, current_version, created_at`

func scanBot(row interface{ Scan(...any) error }) (Bot, error) {
	var b Bot
	var created string
	err := row.Scan(&b.ID, &b.Name, &b.Username, &b.TgID, &b.State, &b.Port, &b.Version, &created)
	b.CreatedAt, _ = store.ParseTime(created)
	return b, err
}

// Bots returns all bots.
func (c *Core) Bots(ctx context.Context) ([]Bot, error) {
	rows, err := c.DB.R.QueryContext(ctx, `SELECT `+botColumns+` FROM bots ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bot
	for rows.Next() {
		b, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Bot returns one bot.
func (c *Core) Bot(ctx context.Context, id int64) (Bot, error) {
	b, err := scanBot(c.DB.R.QueryRowContext(ctx, `SELECT `+botColumns+` FROM bots WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return b, errNoBot
	}
	return b, err
}

// BotByName finds a bot by its panel name or @username.
func (c *Core) BotByName(ctx context.Context, name string) (Bot, error) {
	name = strings.TrimPrefix(name, "@")
	b, err := scanBot(c.DB.R.QueryRowContext(ctx, `SELECT `+botColumns+` FROM bots WHERE name = ? OR username = ?`, name, name))
	if errors.Is(err, sql.ErrNoRows) {
		return b, errNoBot
	}
	return b, err
}

func validBotName(name string) error {
	if strings.TrimSpace(name) == "" || len(name) > 64 {
		return wire.Errorf(wire.CodeBadRequest, "name must be 1-64 characters")
	}
	return nil
}

// freePort returns the lowest bot port not taken; call inside the write tx.
func (c *Core) freePort(ctx context.Context, tx *sql.Tx) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT port FROM bots ORDER BY port`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var p int
		rows.Scan(&p)
		used[p] = true
	}
	for p := c.Cfg.BotPortFirst; p <= c.Cfg.BotPortLast; p++ {
		// Each bot needs two ports: its own and its canary's.
		if !used[p] && p%2 == c.Cfg.BotPortFirst%2 {
			return p, nil
		}
	}
	return 0, errors.New("no free bot ports")
}

func randomSecret() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateBot checks the token with getMe and stores the bot. Nothing is
// saved if Telegram rejects the token.
func (c *Core) CreateBot(ctx context.Context, in CreateBot) (Bot, error) {
	if err := validBotName(in.Name); err != nil {
		return Bot{}, err
	}
	in.Token = strings.TrimSpace(in.Token)
	me, err := c.TG.GetMe(ctx, in.Token)
	if err != nil {
		return Bot{}, telegramError(err)
	}
	c.Sec.Redact.Add(in.Token)
	var b Bot
	err = c.DB.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM bots WHERE tg_id = ?`, me.ID).Scan(&n)
		if n > 0 {
			return wire.Errorf("exists", "@%s is already in the panel", me.Username)
		}
		port, err := c.freePort(ctx, tx)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO bots (name, username, tg_id, state, port, created_at) VALUES (?, ?, ?, 'stopped', ?, ?)`,
			in.Name, me.Username, me.ID, port, store.Now())
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return wire.Errorf("exists", "a bot named %q already exists", in.Name)
			}
			return err
		}
		id, _ := res.LastInsertId()
		if err := c.Sec.SetTx(ctx, tx, id, secrets.TokenName, secrets.KindValue, []byte(in.Token)); err != nil {
			return err
		}
		if err := c.Sec.SetTx(ctx, tx, id, secrets.WebhookSecretName, secrets.KindValue, []byte(randomSecret())); err != nil {
			return err
		}
		b = Bot{ID: id, Name: in.Name, Username: me.Username, TgID: me.ID, State: StateStopped, Port: port, CreatedAt: time.Now().UTC()}
		return nil
	})
	if err != nil {
		return Bot{}, err
	}
	c.Log.Info("bot created", "bot", b.ID, "username", b.Username)
	return b, nil
}

// SetToken checks a new token with getMe and replaces the old one. If the
// bot runs, the old token's webhook is removed first.
func (c *Core) SetToken(ctx context.Context, in SetToken) (Bot, error) {
	b, err := c.Bot(ctx, in.BotID)
	if err != nil {
		return b, err
	}
	in.Token = strings.TrimSpace(in.Token)
	me, err := c.TG.GetMe(ctx, in.Token)
	if err != nil {
		return b, telegramError(err)
	}
	c.Sec.Redact.Add(in.Token)
	if old, err := c.Sec.Decrypt(ctx, b.ID, secrets.TokenName); err == nil && string(old) != in.Token && b.State != StateStopped {
		if err := c.TG.DeleteWebhook(ctx, string(old), false); err != nil {
			c.Log.Warn("deleteWebhook for the old token failed", "bot", b.ID, "err", err)
		}
	}
	err = c.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE bots SET username = ?, tg_id = ? WHERE id = ?`, me.Username, me.ID, b.ID); err != nil {
			return err
		}
		return c.Sec.SetTx(ctx, tx, b.ID, secrets.TokenName, secrets.KindValue, []byte(in.Token))
	})
	if err != nil {
		return b, err
	}
	b.Username, b.TgID = me.Username, me.ID
	return b, nil
}

// Token returns the decrypted bot token (inside the core only).
func (c *Core) Token(ctx context.Context, botID int64) (string, error) {
	t, err := c.Sec.Decrypt(ctx, botID, secrets.TokenName)
	if err != nil {
		return "", fmt.Errorf("bot %d token: %w", botID, err)
	}
	return string(t), nil
}

func (c *Core) registerBots() {
	c.Mux.Handle("bots.create", wire.Func(func(ctx context.Context, _ *wire.Conn, in CreateBot) (b Bot, err error) {
		err = c.Journal.Do(ctx, in.Actor, "bots.create", 0, map[string]string{"name": in.Name}, func() error {
			b, err = c.CreateBot(ctx, in)
			return err
		})
		return b, err
	}))
	c.Mux.Handle("bots.set_token", wire.Func(func(ctx context.Context, _ *wire.Conn, in SetToken) (b Bot, err error) {
		err = c.Journal.Do(ctx, in.Actor, "bots.set_token", in.BotID, map[string]string{"token": "изменено"}, func() error {
			b, err = c.SetToken(ctx, in)
			return err
		})
		return b, err
	}))
	c.Mux.Handle("bots.list", wire.Func(func(ctx context.Context, _ *wire.Conn, _ struct{}) ([]Bot, error) {
		return c.Bots(ctx)
	}))
	c.Mux.Handle("bots.get", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (Bot, error) {
		return c.Bot(ctx, id)
	}))
}
