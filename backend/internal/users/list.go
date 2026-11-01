package users

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// User is a row of the "Users" tab. Username comes from strangers: it is
// untrusted data.
type User struct {
	ID        int64     `cbor:"1,keyasint" json:"id"`
	Username  string    `cbor:"2,keyasint" json:"username"`
	Language  string    `cbor:"3,keyasint" json:"language"`
	Premium   bool      `cbor:"4,keyasint" json:"premium"`
	Source    string    `cbor:"5,keyasint" json:"source"`
	FirstSeen time.Time `cbor:"6,keyasint" json:"first_seen"`
	LastSeen  time.Time `cbor:"7,keyasint" json:"last_seen"`
	Updates   int64     `cbor:"8,keyasint" json:"updates"`
	Blocked   bool      `cbor:"9,keyasint" json:"blocked"`
	Banned    bool      `cbor:"10,keyasint" json:"banned"`
	BanReason string    `cbor:"11,keyasint" json:"ban_reason,omitempty"`
	BanUntil  time.Time `cbor:"12,keyasint" json:"ban_until,omitzero"`
	Days      int       `cbor:"13,keyasint" json:"active_days"`
}

// ListQuery filters the list.
type ListQuery struct {
	BotID  int64  `cbor:"1,keyasint"`
	Query  string `cbor:"2,keyasint,omitempty"` // id or part of username
	Filter string `cbor:"3,keyasint,omitempty"` // all | banned | blocked
	Limit  int    `cbor:"4,keyasint,omitempty"`
	Offset int    `cbor:"5,keyasint,omitempty"`
}

// ListResult is a page.
type ListResult struct {
	Users []User `cbor:"1,keyasint" json:"users"`
	Total int64  `cbor:"2,keyasint" json:"total"`
}

func timeOrZero(s sql.NullString) time.Time {
	if !s.Valid {
		return time.Time{}
	}
	t, _ := store.ParseTime(s.String)
	return t
}

// List returns users of a bot, the most recent first.
func (t *Tracker) List(ctx context.Context, q ListQuery) (ListResult, error) {
	where := []string{"b.bot_id = ?"}
	args := []any{q.BotID}
	if s := strings.TrimSpace(strings.TrimPrefix(q.Query, "@")); s != "" {
		if id, err := strconv.ParseInt(s, 10, 64); err == nil {
			where, args = append(where, "b.user_id = ?"), append(args, id)
		} else {
			where, args = append(where, "g.username LIKE ?"), append(args, "%"+s+"%")
		}
	}
	switch q.Filter {
	case "banned":
		where = append(where, "b.banned")
	case "blocked":
		where = append(where, "b.blocked_at IS NOT NULL")
	}
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	cond := strings.Join(where, " AND ")
	var res ListResult
	if err := t.DB.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM bot_users b LEFT JOIN tg_users g ON g.user_id = b.user_id WHERE `+cond, args...).Scan(&res.Total); err != nil {
		return res, err
	}
	rows, err := t.DB.R.QueryContext(ctx, `SELECT b.user_id, COALESCE(g.username, ''), COALESCE(g.language_code, ''), COALESCE(g.is_premium, 0),
		b.source, b.first_seen, b.last_seen, b.updates_total, b.blocked_at IS NOT NULL, b.banned, b.ban_reason, b.ban_until, b.active_days
		FROM bot_users b LEFT JOIN tg_users g ON g.user_id = b.user_id WHERE `+cond+` ORDER BY b.last_seen DESC LIMIT ? OFFSET ?`,
		append(args, q.Limit, q.Offset)...)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	res.Users = []User{}
	for rows.Next() {
		var u User
		var first, last string
		var until sql.NullString
		var bits []byte
		if err := rows.Scan(&u.ID, &u.Username, &u.Language, &u.Premium, &u.Source, &first, &last, &u.Updates,
			&u.Blocked, &u.Banned, &u.BanReason, &until, &bits); err != nil {
			return res, err
		}
		u.FirstSeen, _ = store.ParseTime(first)
		u.LastSeen, _ = store.ParseTime(last)
		u.BanUntil = timeOrZero(until)
		u.Days = CountActive(bits)
		res.Users = append(res.Users, u)
	}
	return res, rows.Err()
}

// Share is one row of an audience breakdown.
type Share struct {
	Key   string `cbor:"1,keyasint" json:"key"`
	Count int64  `cbor:"2,keyasint" json:"count"`
}

// Audience is the breakdown of a bot's users.
type Audience struct {
	Total     int64   `cbor:"1,keyasint" json:"total"`
	Premium   int64   `cbor:"2,keyasint" json:"premium"`
	Languages []Share `cbor:"3,keyasint" json:"languages"`
	Sources   []Share `cbor:"4,keyasint" json:"sources"`
}

func (t *Tracker) shares(ctx context.Context, q string, args ...any) ([]Share, error) {
	rows, err := t.DB.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		var s Share
		if err := rows.Scan(&s.Key, &s.Count); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Audience returns languages, Premium share and sources.
func (t *Tracker) Audience(ctx context.Context, botID int64) (Audience, error) {
	var a Audience
	err := t.DB.R.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(g.is_premium), 0) FROM bot_users b
		LEFT JOIN tg_users g ON g.user_id = b.user_id WHERE b.bot_id = ?`, botID).Scan(&a.Total, &a.Premium)
	if err != nil {
		return a, err
	}
	if a.Languages, err = t.shares(ctx, `SELECT COALESCE(NULLIF(g.language_code, ''), '?'), COUNT(*) FROM bot_users b
		LEFT JOIN tg_users g ON g.user_id = b.user_id WHERE b.bot_id = ? GROUP BY 1 ORDER BY 2 DESC LIMIT 20`, botID); err != nil {
		return a, err
	}
	a.Sources, err = t.shares(ctx, `SELECT source, COUNT(*) FROM bot_users WHERE bot_id = ? AND source != ''
		GROUP BY source ORDER BY 2 DESC LIMIT 50`, botID)
	return a, err
}

func (t *Tracker) registerList(mux *wire.Mux) {
	mux.Handle("users.list", wire.Func(func(ctx context.Context, _ *wire.Conn, q ListQuery) (ListResult, error) {
		return t.List(ctx, q)
	}))
	mux.Handle("users.audience", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (Audience, error) {
		return t.Audience(ctx, id)
	}))
}
