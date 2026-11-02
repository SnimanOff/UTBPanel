// Package secretreq keeps secrets an AI asked for (request_secret, the
// token of a bot created through MCP). The AI never passes values: they
// would stay in the chat history and with the model provider. The owner
// enters the value in the panel; the AI sees only the state.
package secretreq

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// TTL is how long a request waits for the owner.
var TTL = 24 * time.Hour

// TokenName is the request for the token of a new bot (BotID 0).
const TokenName = secrets.TokenName

// Request is one secret an AI waits for.
type Request struct {
	ID         string    `json:"id"`
	BotID      int64     `json:"bot_id,omitempty"`
	Name       string    `json:"name"`
	Hint       string    `json:"hint"`
	BotName    string    `json:"bot_name,omitempty"`
	GroupID    int64     `json:"group_id,omitempty"`
	Requested  string    `json:"requested_by"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	State      string    `json:"state"` // waiting | set | rejected | expired
	CreatedBot int64     `json:"created_bot,omitempty"`
	DecidedBy  string    `json:"decided_by,omitempty"`
	DecidedAt  time.Time `json:"decided_at,omitzero"`

	// Serial is the device that asked; only it follows the request.
	Serial string `json:"-"`
}

// Final reports whether the request will not change any more.
func (r Request) Final() bool { return r.State != "waiting" }

// Store keeps requests in panel.db.
type Store struct {
	DB    *store.DB
	Notes *alerts.Service

	mu      sync.Mutex
	changed chan struct{}
}

// ErrDecided is returned for a request that is no longer waiting.
var ErrDecided = errors.New("запрос уже закрыт или истёк")

const columns = `id, bot_id, name, hint, bot_name, group_id, caller, created_at, expires_at, state, created_bot, decided_by, decided_at`

func scan(row interface{ Scan(...any) error }) (Request, error) {
	var r Request
	var caller string
	var created, expires, decided int64
	if err := row.Scan(&r.ID, &r.BotID, &r.Name, &r.Hint, &r.BotName, &r.GroupID, &caller, &created, &expires, &r.State, &r.CreatedBot, &r.DecidedBy, &decided); err != nil {
		return r, err
	}
	var c api.Caller
	json.Unmarshal([]byte(caller), &c)
	r.Requested, r.Serial = c.Name, c.Serial
	r.CreatedAt, r.ExpiresAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC()
	if decided > 0 {
		r.DecidedAt = time.Unix(decided, 0).UTC()
	}
	return r, nil
}

// Wake returns a channel closed on the next change of any request.
func (s *Store) Wake() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}

func (s *Store) signal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changed != nil {
		close(s.changed)
	}
	s.changed = make(chan struct{})
}

func href(r Request) string {
	if r.BotID == 0 {
		return "/ai"
	}
	return "/bots/" + strconv.FormatInt(r.BotID, 10) + "/settings?secret=" + r.Name
}

// Ask records a request and notifies the owner. A request for the same
// secret that still waits is returned instead of a second one.
func (s *Store) Ask(ctx context.Context, caller api.Caller, botID int64, name, hint, botName string, groupID int64) (Request, error) {
	if botID == 0 {
		name = TokenName
	} else if !secrets.ValidName(name) || name == secrets.TokenName || name == secrets.WebhookSecretName || name == secrets.GitHubTokenName || name == secrets.GitHubHookName {
		return Request{}, fmt.Errorf("invalid secret name %q", name)
	}
	if len([]rune(hint)) > 300 {
		hint = string([]rune(hint)[:300])
	}
	s.expire(ctx)
	if botID != 0 {
		r, err := scan(s.DB.R.QueryRowContext(ctx, `SELECT `+columns+` FROM secret_requests WHERE bot_id = ? AND name = ? AND state = 'waiting'`, botID, name))
		if err == nil {
			return r, nil
		}
	}
	b := make([]byte, 12)
	rand.Read(b)
	id := hex.EncodeToString(b)
	rawCaller, _ := json.Marshal(caller)
	now := time.Now()
	if _, err := s.DB.W.ExecContext(ctx, `INSERT INTO secret_requests (id, bot_id, name, hint, bot_name, group_id, caller, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, botID, name, hint, botName, groupID, string(rawCaller), now.Unix(), now.Add(TTL).Unix()); err != nil {
		return Request{}, err
	}
	r, err := s.Get(ctx, id)
	if err != nil {
		return r, err
	}
	if s.Notes != nil {
		text := "нейросеть просит " + name
		if botID == 0 {
			text = "нейросеть просит токен нового бота «" + botName + "»"
		}
		if hint != "" {
			text += ": " + hint
		}
		s.Notes.Notify(context.WithoutCancel(ctx), botID, alerts.Note{Kind: "mcp_secret", Severity: "warn", Key: "secret:" + id, Text: text, Href: href(r)})
	}
	s.signal()
	return r, nil
}

// Get returns one request.
func (s *Store) Get(ctx context.Context, id string) (Request, error) {
	s.expire(ctx)
	return scan(s.DB.R.QueryRowContext(ctx, `SELECT `+columns+` FROM secret_requests WHERE id = ?`, id))
}

// List returns requests of a bot (botID < 0: of all bots) from the last day,
// waiting ones first; waiting=true leaves only those.
func (s *Store) List(ctx context.Context, botID int64, waiting bool) ([]Request, error) {
	s.expire(ctx)
	q := `SELECT ` + columns + ` FROM secret_requests WHERE (? < 0 OR bot_id = ?) AND (state = 'waiting' OR (? = 0 AND created_at > ?))
		ORDER BY state = 'waiting' DESC, created_at DESC LIMIT 100`
	only := 0
	if waiting {
		only = 1
	}
	rows, err := s.DB.R.QueryContext(ctx, q, botID, botID, only, time.Now().Add(-TTL).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) close(ctx context.Context, where string, state, by string, createdBot int64, args ...any) ([]string, error) {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT id FROM secret_requests WHERE state = 'waiting' AND `+where, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var closed []string
	for _, id := range ids {
		res, err := s.DB.W.ExecContext(ctx, `UPDATE secret_requests SET state = ?, decided_by = ?, decided_at = ?, created_bot = ? WHERE id = ? AND state = 'waiting'`,
			state, by, time.Now().Unix(), createdBot, id)
		if err != nil {
			return closed, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			closed = append(closed, id)
			if s.Notes != nil {
				s.Notes.Close(context.WithoutCancel(ctx), "secret:"+id)
			}
		}
	}
	if len(closed) > 0 {
		s.signal()
	}
	return closed, nil
}

// Fulfilled closes requests for a secret the owner has just set.
func (s *Store) Fulfilled(ctx context.Context, botID int64, name, by string) {
	s.close(ctx, `bot_id = ? AND name = ?`, "set", by, 0, botID, name)
}

// Reject closes a waiting request.
func (s *Store) Reject(ctx context.Context, id, by string) error {
	s.expire(ctx)
	closed, err := s.close(ctx, `id = ?`, "rejected", by, 0, id)
	if err != nil {
		return err
	}
	if len(closed) == 0 {
		if _, err := s.Get(ctx, id); errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return ErrDecided
	}
	return nil
}

// Created closes a new-bot request with the bot made from its token.
func (s *Store) Created(ctx context.Context, id string, botID int64, by string) error {
	closed, err := s.close(ctx, `id = ? AND bot_id = 0`, "set", by, botID, id)
	if err == nil && len(closed) == 0 {
		return ErrDecided
	}
	return err
}

func (s *Store) expire(ctx context.Context) {
	s.close(ctx, `expires_at <= ?`, "expired", "", 0, time.Now().Unix())
}

// RunExpiry closes expired requests and their notifications every minute.
func (s *Store) RunExpiry(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.expire(ctx)
		}
	}
}

// ValidID tells request ids from anything else in a path.
func ValidID(id string) bool {
	if len(id) != 24 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
