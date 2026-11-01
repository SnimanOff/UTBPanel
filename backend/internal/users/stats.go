package users

import (
	"context"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Stats are user numbers the panel shows next to the charts. Retention is
// exact (from the activity bitmaps), so it comes from the core.
type Stats struct {
	Total   int64   `cbor:"1,keyasint" json:"total"`
	Blocked int64   `cbor:"2,keyasint" json:"blocked"`
	Banned  int64   `cbor:"3,keyasint" json:"banned"`
	D1      float64 `cbor:"4,keyasint" json:"d1"`
	D7      float64 `cbor:"5,keyasint" json:"d7"`
	D30     float64 `cbor:"6,keyasint" json:"d30"`
	Premium int64   `cbor:"7,keyasint" json:"premium"`
}

// Stats computes the numbers of a bot.
func (t *Tracker) Stats(ctx context.Context, botID int64) (Stats, error) {
	var s Stats
	err := t.DB.R.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(blocked_at), SUM(banned),
		COALESCE((SELECT COUNT(*) FROM tg_users g JOIN bot_users b2 ON b2.user_id = g.user_id WHERE b2.bot_id = ? AND g.is_premium), 0)
		FROM bot_users WHERE bot_id = ?`, botID, botID).Scan(&s.Total, &s.Blocked, nullZero{&s.Banned}, &s.Premium)
	if err != nil {
		return s, err
	}
	now := time.Now()
	s.D1, _, _ = t.Retention(ctx, botID, 1, now)
	s.D7, _, _ = t.Retention(ctx, botID, 7, now)
	s.D30, _, _ = t.Retention(ctx, botID, 30, now)
	return s, nil
}

type nullZero struct{ p *int64 }

func (n nullZero) Scan(v any) error {
	if x, ok := v.(int64); ok {
		*n.p = x
	}
	return nil
}

// Register adds users.stats to the core socket.
func (t *Tracker) Register(mux *wire.Mux) {
	t.registerList(mux)
	mux.Handle("users.stats", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (Stats, error) {
		return t.Stats(ctx, id)
	}))
}
