package users

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/updrules"
)

// DefaultClosedText is answered to users outside the list.
const DefaultClosedText = "Бот пока доступен не всем"

// ClosedState is the closed mode of one bot.
type ClosedState struct {
	Enabled bool      `cbor:"1,keyasint" json:"enabled"`
	Text    string    `cbor:"2,keyasint" json:"text"`
	Allow   []Allowed `cbor:"3,keyasint" json:"allow"`
}

// Allowed is one user of the list.
type Allowed struct {
	UserID   int64  `cbor:"1,keyasint" json:"user_id"`
	Username string `cbor:"2,keyasint" json:"username,omitempty"`
	Note     string `cbor:"3,keyasint" json:"note,omitempty"`
}

// Closed answers only users from the list (step 8); the rest get the text.
// Handy to try a new version in production while the bot is only yours.
type Closed struct {
	DB *sql.DB // read pool of core.db
	W  interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}

	mu    sync.RWMutex
	cache map[int64]*closedCache
}

type closedCache struct {
	enabled bool
	text    string
	allow   map[int64]bool
}

// settings column: {"closed": {"enabled": true, "text": "…"}}
type closedSettings struct {
	Enabled bool   `json:"enabled"`
	Text    string `json:"text"`
}

func (c *Closed) load(ctx context.Context, bot int64) *closedCache {
	c.mu.RLock()
	cc := c.cache[bot]
	c.mu.RUnlock()
	if cc != nil {
		return cc
	}
	cc = &closedCache{allow: map[int64]bool{}}
	var raw sql.NullString
	c.DB.QueryRowContext(ctx, `SELECT json_extract(settings, '$.closed') FROM bots WHERE id = ?`, bot).Scan(&raw)
	if raw.Valid {
		var s closedSettings
		json.Unmarshal([]byte(raw.String), &s)
		cc.enabled, cc.text = s.Enabled, s.Text
	}
	if cc.text == "" {
		cc.text = DefaultClosedText
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT user_id FROM bot_allow WHERE bot_id = ?`, bot)
	if err == nil {
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			cc.allow[id] = true
		}
		rows.Close()
	}
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[int64]*closedCache{}
	}
	c.cache[bot] = cc
	c.mu.Unlock()
	return cc
}

// Invalidate drops the cache of a bot after a change.
func (c *Closed) Invalidate(bot int64) {
	c.mu.Lock()
	delete(c.cache, bot)
	c.mu.Unlock()
}

// Check is pipeline step 8.
func (c *Closed) Check(ctx context.Context, u *ingress.Update) ingress.Verdict {
	cc := c.load(ctx, u.BotID)
	if !cc.enabled || u.UserID == 0 || cc.allow[u.UserID] {
		return ingress.Verdict{}
	}
	return ingress.Verdict{Stop: true, Reason: updrules.Closed, Text: cc.text}
}

// State returns the closed mode and the list with usernames.
func (c *Closed) State(ctx context.Context, bot int64) (ClosedState, error) {
	cc := c.load(ctx, bot)
	st := ClosedState{Enabled: cc.enabled, Text: cc.text, Allow: []Allowed{}}
	rows, err := c.DB.QueryContext(ctx, `SELECT a.user_id, COALESCE(g.username, ''), a.note FROM bot_allow a
		LEFT JOIN tg_users g ON g.user_id = a.user_id WHERE a.bot_id = ? ORDER BY a.user_id`, bot)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Allowed
		rows.Scan(&a.UserID, &a.Username, &a.Note)
		st.Allow = append(st.Allow, a)
	}
	return st, rows.Err()
}

// Set switches closed mode and its text.
func (c *Closed) Set(ctx context.Context, bot int64, enabled bool, text string) error {
	b, _ := json.Marshal(closedSettings{Enabled: enabled, Text: strings.TrimSpace(text)})
	_, err := c.W.ExecContext(ctx, `UPDATE bots SET settings = json_set(settings, '$.closed', json(?)) WHERE id = ?`, string(b), bot)
	c.Invalidate(bot)
	return err
}

// ErrUnknownUser: @username works only for users who already wrote to the bot.
var ErrUnknownUser = errors.New("users: no such user; @username works only for those who already wrote to the bot")

// Resolve turns "123" or "@name" into a user id.
func (c *Closed) Resolve(ctx context.Context, bot int64, who string) (int64, error) {
	who = strings.TrimSpace(who)
	if id, err := strconv.ParseInt(who, 10, 64); err == nil && id > 0 {
		return id, nil
	}
	name := strings.TrimPrefix(who, "@")
	var id int64
	err := c.DB.QueryRowContext(ctx, `SELECT g.user_id FROM tg_users g JOIN bot_users b ON b.user_id = g.user_id
		WHERE b.bot_id = ? AND lower(g.username) = lower(?)`, bot, name).Scan(&id)
	if err != nil {
		return 0, ErrUnknownUser
	}
	return id, nil
}

// Allow adds a user to the list.
func (c *Closed) Allow(ctx context.Context, bot, user int64, note string) error {
	_, err := c.W.ExecContext(ctx, `INSERT INTO bot_allow (bot_id, user_id, note) VALUES (?, ?, ?)
		ON CONFLICT (bot_id, user_id) DO UPDATE SET note = excluded.note`, bot, user, note)
	c.Invalidate(bot)
	return err
}

// Remove takes a user off the list.
func (c *Closed) Remove(ctx context.Context, bot, user int64) error {
	_, err := c.W.ExecContext(ctx, `DELETE FROM bot_allow WHERE bot_id = ? AND user_id = ?`, bot, user)
	c.Invalidate(bot)
	return err
}
