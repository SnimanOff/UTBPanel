// Package broadcast sends one message to the users of a bot: text with
// Telegram formatting, an optional picture and link buttons. The core runs
// it; recipients are all users or the active ones, never those who blocked
// the bot or are banned. Progress is saved as it goes, so a restart of the
// core continues the broadcast.
package broadcast

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
)

// States of a broadcast.
const (
	Scheduled = "scheduled"
	Running   = "running"
	Paused    = "paused"
	Done      = "done"
	Cancelled = "cancelled"
)

// Button is a link under the message.
type Button struct {
	Text string `cbor:"1,keyasint" json:"text"`
	URL  string `cbor:"2,keyasint" json:"url"`
}

// B is a broadcast with its progress.
type B struct {
	ID         int64          `cbor:"1,keyasint" json:"id"`
	BotID      int64          `cbor:"2,keyasint" json:"bot_id"`
	Text       string         `cbor:"3,keyasint" json:"text"`
	ParseMode  string         `cbor:"4,keyasint" json:"parse_mode"`
	Photo      []byte         `cbor:"5,keyasint,omitempty" json:"-"`
	PhotoName  string         `cbor:"6,keyasint,omitempty" json:"photo_name,omitempty"`
	Buttons    []Button       `cbor:"7,keyasint" json:"buttons"`
	Audience   string         `cbor:"8,keyasint" json:"audience"` // all | active
	ActiveDays int            `cbor:"9,keyasint" json:"active_days,omitempty"`
	StartsAt   time.Time      `cbor:"10,keyasint" json:"starts_at"`
	State      string         `cbor:"11,keyasint" json:"state"`
	Kind       string         `cbor:"12,keyasint,omitempty" json:"kind,omitempty"`
	CreatedBy  string         `cbor:"13,keyasint" json:"created_by"`
	CreatedAt  time.Time      `cbor:"14,keyasint" json:"created_at"`
	StartedAt  time.Time      `cbor:"15,keyasint,omitzero" json:"started_at,omitzero"`
	FinishedAt time.Time      `cbor:"16,keyasint,omitzero" json:"finished_at,omitzero"`
	Total      int            `cbor:"17,keyasint" json:"total"`
	Sent       int            `cbor:"18,keyasint" json:"sent"`
	Blocked    int            `cbor:"19,keyasint" json:"blocked"`
	Failed     int            `cbor:"20,keyasint" json:"failed"`
	Errors     map[string]int `cbor:"21,keyasint" json:"errors"`
	// Revision is editing or recalling after sending:
	// '' | editing | deleting | edited | deleted.
	Revision  string `cbor:"22,keyasint" json:"revision"`
	RevDone   int    `cbor:"23,keyasint" json:"rev_done"`
	RevFailed int    `cbor:"24,keyasint" json:"rev_failed"`
	// RevisableUntil: edit and recall work until then (48 h after start).
	RevisableUntil time.Time `cbor:"25,keyasint,omitzero" json:"revisable_until,omitzero"`
	// DeleteAfter recalls the broadcast this many hours after it started,
	// 0 = never; DeleteAt is when (from the start, or the planned start).
	DeleteAfter int       `cbor:"26,keyasint,omitempty" json:"delete_after,omitempty"`
	DeleteAt    time.Time `cbor:"27,keyasint,omitzero" json:"delete_at,omitzero"`
	cursor      int64
	revCursor   int64
	sent        []sentMessage // delivered since the last save
	fileID      string
}

// Sender is the part of the Telegram client broadcasts use.
type Sender interface {
	SendMessage(ctx context.Context, token string, p telegram.SendMessage) (telegram.Message, error)
	SendPhoto(ctx context.Context, token string, p telegram.SendPhoto) (telegram.Message, error)
}

// Service keeps and runs broadcasts.
type Service struct {
	DB    *store.DB
	TG    Sender
	Limit *Limiter
	Token func(ctx context.Context, botID int64) (string, error)
	// Event reports finished broadcasts.
	Event func(botID int64, typ string, data map[string]any)
	Log   *slog.Logger
	// SaveEvery is how often progress is written (messages).
	SaveEvery int

	mu      sync.Mutex
	running map[int64]context.CancelFunc
	wg      sync.WaitGroup
}

// Errors of the service.
var (
	ErrEmpty    = errors.New("broadcast: no text")
	ErrNotFound = errors.New("broadcast: not found")
	ErrState    = errors.New("broadcast: not possible in this state")
	// ErrDeleteAfter: the auto-delete delay is out of range.
	ErrDeleteAfter = errors.New("broadcast: delete_after must be 0..47 hours")
)

const columns = `id, bot_id, text, parse_mode, photo, photo_name, photo_file_id, buttons, audience, active_days, starts_at, state, kind,
	created_by, created_at, COALESCE(started_at, ''), COALESCE(finished_at, ''), cursor, total, sent, blocked, failed, errors,
	revision, rev_cursor, rev_done, rev_failed, delete_after`

func scan(row interface{ Scan(...any) error }) (B, error) {
	var b B
	var buttons, errs, starts, created, started, finished string
	err := row.Scan(&b.ID, &b.BotID, &b.Text, &b.ParseMode, &b.Photo, &b.PhotoName, &b.fileID, &buttons, &b.Audience, &b.ActiveDays,
		&starts, &b.State, &b.Kind, &b.CreatedBy, &created, &started, &finished, &b.cursor, &b.Total, &b.Sent, &b.Blocked, &b.Failed, &errs,
		&b.Revision, &b.revCursor, &b.RevDone, &b.RevFailed, &b.DeleteAfter)
	if err != nil {
		return b, err
	}
	json.Unmarshal([]byte(buttons), &b.Buttons)
	json.Unmarshal([]byte(errs), &b.Errors)
	b.StartsAt, _ = store.ParseTime(starts)
	b.CreatedAt, _ = store.ParseTime(created)
	b.StartedAt, _ = store.ParseTime(started)
	b.FinishedAt, _ = store.ParseTime(finished)
	if !b.StartedAt.IsZero() && b.Sent > 0 && b.Revision != Deleted && b.Kind == "" {
		b.RevisableUntil = b.StartedAt.Add(RevisableFor)
	}
	if b.DeleteAfter > 0 {
		from := b.StartedAt
		if from.IsZero() {
			from = b.StartsAt
		}
		b.DeleteAt = from.Add(time.Duration(b.DeleteAfter) * time.Hour)
	}
	if b.Buttons == nil {
		b.Buttons = []Button{}
	}
	if b.Errors == nil {
		b.Errors = map[string]int{}
	}
	return b, nil
}

// Create stores a broadcast; a zero StartsAt means now.
func (s *Service) Create(ctx context.Context, b B) (B, error) {
	if b.Text == "" && len(b.Photo) == 0 {
		return b, ErrEmpty
	}
	if b.ParseMode == "" {
		b.ParseMode = "HTML"
	}
	if b.Audience != "active" {
		b.Audience, b.ActiveDays = "all", 0
	}
	if b.DeleteAfter < 0 || b.DeleteAfter > MaxDeleteAfter {
		return b, ErrDeleteAfter
	}
	now := time.Now().UTC()
	if b.StartsAt.IsZero() || b.StartsAt.Before(now) {
		b.StartsAt = now
	}
	buttons, _ := json.Marshal(nonNil(b.Buttons))
	res, err := s.DB.W.ExecContext(ctx, `INSERT INTO broadcasts (bot_id, text, parse_mode, photo, photo_name, buttons, audience, active_days, starts_at, state, kind, created_by, created_at, delete_after)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.BotID, b.Text, b.ParseMode, b.Photo, b.PhotoName, string(buttons), b.Audience, b.ActiveDays,
		store.FormatTime(b.StartsAt), Scheduled, b.Kind, b.CreatedBy, store.FormatTime(now), b.DeleteAfter)
	if err != nil {
		return b, err
	}
	id, _ := res.LastInsertId()
	return s.Get(ctx, id)
}

func nonNil(b []Button) []Button {
	if b == nil {
		return []Button{}
	}
	return b
}

// Get returns one broadcast.
func (s *Service) Get(ctx context.Context, id int64) (B, error) {
	b, err := scan(s.DB.R.QueryRowContext(ctx, `SELECT `+columns+` FROM broadcasts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// List returns the history of a bot, newest first.
func (s *Service) List(ctx context.Context, botID int64, limit int) ([]B, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.R.QueryContext(ctx, `SELECT `+columns+` FROM broadcasts WHERE bot_id = ? ORDER BY id DESC LIMIT ?`, botID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []B{}
	for rows.Next() {
		b, err := scan(rows)
		if err != nil {
			return nil, err
		}
		b.Photo = nil
		out = append(out, b)
	}
	return out, rows.Err()
}

// audience is the WHERE part for recipients.
func audience(b B) (string, []any) {
	q := `bot_id = ? AND blocked_at IS NULL AND NOT banned`
	args := []any{b.BotID}
	if b.Audience == "active" && b.ActiveDays > 0 {
		q += ` AND last_seen >= ?`
		args = append(args, store.FormatTime(time.Now().Add(-time.Duration(b.ActiveDays)*24*time.Hour)))
	}
	return q, args
}

// Estimate counts recipients for the form.
func (s *Service) Estimate(ctx context.Context, botID int64, aud string, days int) (int, error) {
	q, args := audience(B{BotID: botID, Audience: aud, ActiveDays: days})
	var n int
	err := s.DB.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM bot_users WHERE `+q, args...).Scan(&n)
	return n, err
}

// setState changes the state if it is one of from.
func (s *Service) setState(ctx context.Context, id int64, to string, from ...string) error {
	b, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	ok := false
	for _, f := range from {
		ok = ok || b.State == f
	}
	if !ok {
		return ErrState
	}
	q := `UPDATE broadcasts SET state = ? WHERE id = ?`
	args := []any{to, id}
	if to == Cancelled {
		q = `UPDATE broadcasts SET state = ?, finished_at = ? WHERE id = ?`
		args = []any{to, store.Now(), id}
	}
	if _, err := s.DB.W.ExecContext(ctx, q, args...); err != nil {
		return err
	}
	if to == Paused || to == Cancelled {
		s.stop(id)
	}
	return nil
}

// Pause stops sending; progress is kept.
func (s *Service) Pause(ctx context.Context, id int64) error {
	return s.setState(ctx, id, Paused, Running, Scheduled)
}

// Resume continues a paused broadcast; not while it is being revised.
func (s *Service) Resume(ctx context.Context, id int64) error {
	b, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if b.Revision == Editing || b.Revision == Deleting || b.Revision == Deleted {
		return ErrState
	}
	return s.setState(ctx, id, Running, Paused)
}

// Cancel stops a broadcast for good.
func (s *Service) Cancel(ctx context.Context, id int64) error {
	return s.setState(ctx, id, Cancelled, Running, Scheduled, Paused)
}

func (s *Service) stop(id int64) {
	s.mu.Lock()
	cancel := s.running[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Run starts due broadcasts and continues running ones after a restart.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var pruned time.Time
	for {
		s.Tick(ctx)
		if time.Since(pruned) > time.Hour {
			s.Prune(ctx)
			pruned = time.Now()
		}
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return
		case <-t.C:
		}
	}
}

// Tick launches what is due now.
func (s *Service) Tick(ctx context.Context) {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT id FROM broadcasts WHERE state = ? OR (state = ? AND starts_at <= ?)`,
		Running, Scheduled, store.Now())
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		s.launch(ctx, id, s.send)
	}
	s.autoRecall(ctx)
	// Revisions run under the negative id: a paused broadcast may be
	// recalled while nothing sends it.
	rows, err = s.DB.R.QueryContext(ctx, `SELECT id FROM broadcasts WHERE revision IN (?, ?)`, Editing, Deleting)
	if err != nil {
		return
	}
	ids = ids[:0]
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	_ = rows.Close()
	for _, id := range ids {
		s.launch(ctx, -id, func(ctx context.Context, id int64) error { return s.revise(ctx, -id) })
	}
}

// launch runs fn(key) once at a time per key.
func (s *Service) launch(ctx context.Context, key int64, fn func(context.Context, int64) error) {
	s.mu.Lock()
	if s.running == nil {
		s.running = map[int64]context.CancelFunc{}
	}
	if _, busy := s.running[key]; busy {
		s.mu.Unlock()
		return
	}
	rctx, cancel := context.WithCancel(ctx)
	s.running[key] = cancel
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.running, key)
			s.mu.Unlock()
			cancel()
		}()
		if err := fn(rctx, key); err != nil && !errors.Is(err, context.Canceled) && s.Log != nil {
			s.Log.Error("broadcast failed", "id", key, "err", err)
		}
	}()
}

// Wait waits for running broadcasts (tests).
func (s *Service) Wait() { s.wg.Wait() }
