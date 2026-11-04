package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Payment is a row of the "Payments" list. Payload comes from the bot.
type Payment struct {
	ID         int64     `cbor:"1,keyasint" json:"id"`
	TS         time.Time `cbor:"2,keyasint" json:"ts"`
	UserID     int64     `cbor:"3,keyasint" json:"user_id"`
	Currency   string    `cbor:"4,keyasint" json:"currency"`
	Amount     int64     `cbor:"5,keyasint" json:"amount"`
	Payload    string    `cbor:"6,keyasint" json:"payload"`
	ChargeID   string    `cbor:"7,keyasint" json:"charge_id"`
	RefundedAt time.Time `cbor:"8,keyasint" json:"refunded_at,omitzero"`
	// Refundable: Stars with a charge id, not refunded yet.
	Refundable bool `cbor:"9,keyasint" json:"refundable"`
}

// PaymentsQuery selects payments of a bot, newest first.
type PaymentsQuery struct {
	BotID  int64 `cbor:"1,keyasint"`
	UserID int64 `cbor:"2,keyasint,omitempty"`
	Limit  int   `cbor:"3,keyasint,omitempty"`
	Offset int   `cbor:"4,keyasint,omitempty"`
}

// RefundReq refunds one payment in Stars.
type RefundReq struct {
	BotID     int64         `cbor:"1,keyasint"`
	PaymentID int64         `cbor:"2,keyasint"`
	Actor     journal.Actor `cbor:"9,keyasint"`
}

const paymentCols = `id, ts, user_id, currency, amount, payload, charge_id, refunded_at`

func scanPayment(row interface{ Scan(...any) error }) (Payment, error) {
	var p Payment
	var ts string
	var refunded sql.NullString
	if err := row.Scan(&p.ID, &ts, &p.UserID, &p.Currency, &p.Amount, &p.Payload, &p.ChargeID, &refunded); err != nil {
		return p, err
	}
	p.TS, _ = store.ParseTime(ts)
	if refunded.Valid {
		p.RefundedAt, _ = store.ParseTime(refunded.String)
	}
	p.Refundable = p.Currency == "XTR" && p.ChargeID != "" && !refunded.Valid
	return p, nil
}

func (c *Core) payments(ctx context.Context, q PaymentsQuery) ([]Payment, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	where, args := `bot_id = ?`, []any{q.BotID}
	if q.UserID != 0 {
		where += ` AND user_id = ?`
		args = append(args, q.UserID)
	}
	//nolint:gosec // only constant fragments are joined; values go as parameters
	rows, err := c.DB.R.QueryContext(ctx, `SELECT `+paymentCols+` FROM payments WHERE `+where+` ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Payment{}
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// refund calls refundStarPayment and marks the payment at once; the
// refunded_payment update that follows sets the same mark and counts the
// refund in the metrics.
func (c *Core) refund(ctx context.Context, in RefundReq) (Payment, error) {
	p, err := scanPayment(c.DB.R.QueryRowContext(ctx, `SELECT `+paymentCols+` FROM payments WHERE id = ? AND bot_id = ?`, in.PaymentID, in.BotID))
	if errors.Is(err, sql.ErrNoRows) {
		return p, wire.Errorf(wire.CodeNotFound, "no payment %d", in.PaymentID)
	}
	if err != nil {
		return p, err
	}
	if !p.RefundedAt.IsZero() {
		return p, wire.Errorf(wire.CodeBadRequest, "платёж уже возвращён")
	}
	if p.Currency != "XTR" || p.ChargeID == "" {
		return p, wire.Errorf(wire.CodeBadRequest, "Bot API возвращает только Stars; этот платёж возвращается у платёжного провайдера")
	}
	token, err := c.Token(ctx, in.BotID)
	if err != nil {
		return p, err
	}
	err = c.TG.Call(ctx, token, "refundStarPayment", telegram.Values("user_id", p.UserID, "telegram_payment_charge_id", p.ChargeID), nil)
	if te, ok := telegram.AsError(err); ok {
		if !strings.Contains(te.Description, "CHARGE_ALREADY_REFUNDED") {
			return p, wire.Errorf("telegram", "%s", te.Description)
		}
		// Refunded elsewhere (by the bot itself): just record it.
	} else if err != nil {
		return p, err
	}
	now := time.Now().UTC()
	if _, err := c.DB.W.ExecContext(ctx, `UPDATE payments SET refunded_at = ? WHERE id = ? AND refunded_at IS NULL`, store.FormatTime(now), p.ID); err != nil {
		return p, err
	}
	p.RefundedAt, p.Refundable = now, false
	return p, nil
}

func (c *Core) registerPayments() {
	c.Mux.Handle("payments.list", wire.Func(func(ctx context.Context, _ *wire.Conn, q PaymentsQuery) ([]Payment, error) {
		if err := c.botExists(ctx, q.BotID); err != nil {
			return nil, err
		}
		return c.payments(ctx, q)
	}))
	c.Mux.Handle("payments.refund", wire.Func(func(ctx context.Context, _ *wire.Conn, in RefundReq) (p Payment, err error) {
		err = c.Journal.Do(ctx, in.Actor, "payments.refund", in.BotID, map[string]any{"payment": in.PaymentID}, func() error {
			p, err = c.refund(ctx, in)
			return err
		})
		return p, err
	}))
}
