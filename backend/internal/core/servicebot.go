package core

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// The service bot only writes notifications to the owner. Its token is
// kept like bot tokens: sealed with the master key (bot 0), write-only.
const (
	kvServiceToken    = "service_bot_token" //nolint:gosec // a kv key, not a credential
	kvServiceUsername = "service_bot_username"
	serviceTokenName  = "SERVICE_BOT_TOKEN" //nolint:gosec // the AAD name of the sealed token
)

// ServiceBot is what the panel may know about the service bot.
type ServiceBot struct {
	Configured bool   `cbor:"1,keyasint" json:"configured"`
	Username   string `cbor:"2,keyasint" json:"username"`
}

// ServiceBotSet connects the service bot.
type ServiceBotSet struct {
	Token string        `cbor:"1,keyasint"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

// ServiceBotSend is one message to a chat.
type ServiceBotSend struct {
	ChatID int64  `cbor:"1,keyasint"`
	Text   string `cbor:"2,keyasint"`
}

func (c *Core) kvGet(ctx context.Context, key string) (string, error) {
	var v string
	err := c.DB.R.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	return v, err
}

func (c *Core) kvSetTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO kv (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// serviceToken decrypts the service bot token.
func (c *Core) serviceToken(ctx context.Context) (string, error) {
	v, err := c.kvGet(ctx, kvServiceToken)
	if errors.Is(err, sql.ErrNoRows) {
		return "", wire.Errorf(wire.CodeNotFound, "the service bot is not connected")
	}
	if err != nil {
		return "", err
	}
	nonceHex, ctHex, ok := strings.Cut(v, ":")
	if !ok {
		return "", errors.New("service bot token: bad record")
	}
	nonce, err1 := hex.DecodeString(nonceHex)
	ct, err2 := hex.DecodeString(ctHex)
	if err := errors.Join(err1, err2); err != nil {
		return "", err
	}
	pt, err := c.Sec.Box.Open(0, serviceTokenName, ct, nonce)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

func (c *Core) serviceBot(ctx context.Context) ServiceBot {
	var s ServiceBot
	if _, err := c.kvGet(ctx, kvServiceToken); err == nil {
		s.Configured = true
		s.Username, _ = c.kvGet(ctx, kvServiceUsername)
	}
	return s
}

func (c *Core) setServiceBot(ctx context.Context, token string) (ServiceBot, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return ServiceBot{}, wire.Errorf(wire.CodeBadRequest, "token is empty")
	}
	me, err := c.TG.GetMe(ctx, token)
	if err != nil {
		return ServiceBot{}, wire.Errorf(wire.CodeBadRequest, "getMe: %s", telegram.MaskToken(err.Error()))
	}
	ct, nonce, err := c.Sec.Box.Seal(0, serviceTokenName, []byte(token))
	if err != nil {
		return ServiceBot{}, err
	}
	err = c.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := c.kvSetTx(ctx, tx, kvServiceToken, hex.EncodeToString(nonce)+":"+hex.EncodeToString(ct)); err != nil {
			return err
		}
		return c.kvSetTx(ctx, tx, kvServiceUsername, me.Username)
	})
	return ServiceBot{Configured: true, Username: me.Username}, err
}

func (c *Core) registerServiceBot() {
	c.Mux.Handle("servicebot.get", wire.Func(func(ctx context.Context, _ *wire.Conn, _ struct{}) (ServiceBot, error) {
		return c.serviceBot(ctx), nil
	}))
	c.Mux.Handle("servicebot.set", wire.Func(func(ctx context.Context, _ *wire.Conn, in ServiceBotSet) (s ServiceBot, err error) {
		//nolint:gosec // the journal records that the token changed, not its value
		err = c.Journal.Do(ctx, in.Actor, "servicebot.set", 0, map[string]string{"token": "изменено"}, func() error {
			s, err = c.setServiceBot(ctx, in.Token)
			return err
		})
		return s, err
	}))
	c.Mux.Handle("servicebot.delete", wire.Func(func(ctx context.Context, _ *wire.Conn, actor journal.Actor) (struct{}, error) {
		return struct{}{}, c.Journal.Do(ctx, actor, "servicebot.delete", 0, nil, func() error {
			_, err := c.DB.W.ExecContext(ctx, `DELETE FROM kv WHERE key IN (?, ?)`, kvServiceToken, kvServiceUsername)
			return err
		})
	}))
	c.Mux.Handle("servicebot.send", wire.Func(func(ctx context.Context, _ *wire.Conn, in ServiceBotSend) (struct{}, error) {
		if in.ChatID == 0 {
			return struct{}{}, wire.Errorf(wire.CodeBadRequest, "owner Telegram ID is not set")
		}
		token, err := c.serviceToken(ctx)
		if err != nil {
			return struct{}{}, err
		}
		_, err = c.TG.SendMessage(ctx, token, telegram.SendMessage{ChatID: in.ChatID, Text: in.Text})
		if te, ok := telegram.AsError(err); ok {
			// Telegram's own text, e.g. "Forbidden: bot can't initiate conversation".
			return struct{}{}, wire.Errorf("telegram", "%d: %s", te.Code, te.Description)
		}
		return struct{}{}, err
	}))
}
