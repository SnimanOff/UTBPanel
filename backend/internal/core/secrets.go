package core

import (
	"context"
	"database/sql"
	"errors"

	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// SecretSet is the request to store a secret. There is no request to read
// one: secrets are write-only.
type SecretSet struct {
	BotID int64         `cbor:"1,keyasint"`
	Name  string        `cbor:"2,keyasint"`
	Kind  string        `cbor:"3,keyasint"`
	Value []byte        `cbor:"4,keyasint"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

// SecretRef names one secret.
type SecretRef struct {
	BotID int64         `cbor:"1,keyasint"`
	Name  string        `cbor:"2,keyasint"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

var errNoBot = wire.Errorf(wire.CodeNotFound, "no such bot")

func (c *Core) botExists(ctx context.Context, id int64) error {
	var n int
	if err := c.DB.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM bots WHERE id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return errNoBot
	}
	return nil
}

// reserved secrets have their own operations.
func reserved(name string) bool {
	return name == secrets.TokenName || name == secrets.WebhookSecretName
}

func (c *Core) registerSecrets() {
	c.Mux.Handle("secrets.set", wire.Func(func(ctx context.Context, _ *wire.Conn, in SecretSet) (struct{}, error) {
		if err := c.botExists(ctx, in.BotID); err != nil {
			return struct{}{}, err
		}
		if reserved(in.Name) {
			return struct{}{}, wire.Errorf(wire.CodeBadRequest, "%s is set by its own operation", in.Name)
		}
		if in.Kind == "" {
			in.Kind = secrets.KindValue
		}
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "secrets.set", in.BotID, map[string]string{"name": in.Name, "value": "изменено"}, func() error {
			if err := c.Sec.Set(ctx, in.BotID, in.Name, in.Kind, in.Value); err != nil {
				return wire.Errorf(wire.CodeBadRequest, "%v", err)
			}
			return nil
		})
	}))
	c.Mux.Handle("secrets.delete", wire.Func(func(ctx context.Context, _ *wire.Conn, in SecretRef) (struct{}, error) {
		if reserved(in.Name) {
			return struct{}{}, wire.Errorf(wire.CodeBadRequest, "%s cannot be deleted", in.Name)
		}
		return struct{}{}, c.Journal.Do(ctx, in.Actor, "secrets.delete", in.BotID, map[string]string{"name": in.Name}, func() error {
			err := c.Sec.Delete(ctx, in.BotID, in.Name)
			if errors.Is(err, secrets.ErrNotFound) {
				return wire.Errorf(wire.CodeNotFound, "no secret %s", in.Name)
			}
			return err
		})
	}))
	c.Mux.Handle("secrets.list", wire.Func(func(ctx context.Context, _ *wire.Conn, botID int64) ([]secrets.Info, error) {
		return c.Sec.List(ctx, botID)
	}))
	// Only from the core CLI: core masterkey import.
	c.Mux.Handle("masterkey.import", wire.Func(func(ctx context.Context, conn *wire.Conn, key []byte) (struct{}, error) {
		if conn.Proto() != wire.ProtoCoreCLI {
			return struct{}{}, wire.Errorf("forbidden", "master key import is available only from the core CLI")
		}
		return struct{}{}, c.Journal.Do(ctx, journal.Actor{Source: journal.SourceCLI, Name: "ssh"}, "masterkey.import", 0, nil, func() error {
			return c.importMasterKey(ctx, key)
		})
	}))
}

// HasBots reports whether core.db has any bot.
func HasBots(ctx context.Context, db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bots`).Scan(&n)
	return n > 0, err
}

func (c *Core) importMasterKey(ctx context.Context, key []byte) error {
	has, err := HasBots(ctx, c.DB.R)
	if err != nil {
		return err
	}
	if has {
		return secrets.ErrHasBots
	}
	if err := secrets.WriteMasterKey(c.Cfg.KeysDir, key); err != nil {
		return err
	}
	return c.Sec.Box.SetKey(key)
}
