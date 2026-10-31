package core

import (
	"context"
	"database/sql"
	"errors"

	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// SecretSet is the request to store a secret. There is no request to read
// one: secrets are write-only (plan 3.2).
type SecretSet struct {
	BotID int64  `cbor:"1,keyasint"`
	Name  string `cbor:"2,keyasint"`
	Kind  string `cbor:"3,keyasint"`
	Value []byte `cbor:"4,keyasint"`
}

// SecretRef names one secret.
type SecretRef struct {
	BotID int64  `cbor:"1,keyasint"`
	Name  string `cbor:"2,keyasint"`
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
		if err := c.Sec.Set(ctx, in.BotID, in.Name, in.Kind, in.Value); err != nil {
			return struct{}{}, wire.Errorf(wire.CodeBadRequest, "%v", err)
		}
		return struct{}{}, nil
	}))
	c.Mux.Handle("secrets.delete", wire.Func(func(ctx context.Context, _ *wire.Conn, in SecretRef) (struct{}, error) {
		if reserved(in.Name) {
			return struct{}{}, wire.Errorf(wire.CodeBadRequest, "%s cannot be deleted", in.Name)
		}
		err := c.Sec.Delete(ctx, in.BotID, in.Name)
		if errors.Is(err, secrets.ErrNotFound) {
			return struct{}{}, wire.Errorf(wire.CodeNotFound, "no secret %s", in.Name)
		}
		return struct{}{}, err
	}))
	c.Mux.Handle("secrets.list", wire.Func(func(ctx context.Context, _ *wire.Conn, botID int64) ([]secrets.Info, error) {
		return c.Sec.List(ctx, botID)
	}))
	// Only from the core CLI: core masterkey import.
	c.Mux.Handle("masterkey.import", wire.Func(func(ctx context.Context, conn *wire.Conn, key []byte) (struct{}, error) {
		if conn.Proto() != wire.ProtoCoreCLI {
			return struct{}{}, wire.Errorf("forbidden", "master key import is available only from the core CLI")
		}
		return struct{}{}, c.importMasterKey(ctx, key)
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
