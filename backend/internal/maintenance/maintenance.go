// Package maintenance keeps pause and maintenance of bots: a manual pause
// is maintenance without a schedule or an end. While it is on, users get
// the maintenance text, button presses a window with it, and only payments
// and service updates wait for the bot.
package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/updrules"
)

// DefaultText is used when the owner set none.
const DefaultText = "Идут техработы. Бот скоро вернётся."

// DefaultScheduledText mentions the planned end.
const DefaultScheduledText = "Идут техработы. Бот вернётся примерно в {end} по Москве."

// Kinds and states.
const (
	Pause     = "pause"
	Scheduled = "scheduled"
	Planned   = "planned"
	Active    = "active"
)

// M is the maintenance of one bot.
type M struct {
	BotID     int64     `cbor:"1,keyasint" json:"bot_id"`
	Kind      string    `cbor:"2,keyasint" json:"kind"`
	State     string    `cbor:"3,keyasint" json:"state"`
	StartsAt  time.Time `cbor:"4,keyasint" json:"starts_at,omitzero"`
	EndsAt    time.Time `cbor:"5,keyasint" json:"ends_at,omitzero"`
	Text      string    `cbor:"6,keyasint" json:"text"`
	Version   string    `cbor:"7,keyasint" json:"version,omitempty"`
	AutoEnd   bool      `cbor:"8,keyasint" json:"auto_end,omitempty"`
	WarnHours int       `cbor:"9,keyasint" json:"warn_hours,omitempty"`
	Warned    bool      `cbor:"10,keyasint" json:"warned,omitempty"`
	Overdue   bool      `cbor:"11,keyasint" json:"overdue,omitempty"`
}

// Service keeps maintenance in core.db with a cache for the pipeline.
type Service struct {
	DB *store.DB
	// TZ returns the time zone of a bot's texts.
	TZ func(ctx context.Context, botID int64) *time.Location
	// Changed is called after every change (push to the edge, flush the
	// deferred updates after the end, events).
	Changed func(botID int64, m *M, ended bool)

	mu    sync.RWMutex
	cache map[int64]*M
}

// Load reads all maintenance into memory.
func (s *Service) Load(ctx context.Context) error {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT bot_id, kind, state, starts_at, ends_at, text, version, auto_end, warn_hours, warned, overdue FROM maintenance`)
	if err != nil {
		return err
	}
	defer rows.Close()
	m := map[int64]*M{}
	for rows.Next() {
		var x M
		var st, en sql.NullString
		if err := rows.Scan(&x.BotID, &x.Kind, &x.State, &st, &en, &x.Text, &x.Version, &x.AutoEnd, &x.WarnHours, &x.Warned, &x.Overdue); err != nil {
			return err
		}
		if st.Valid {
			x.StartsAt, _ = store.ParseTime(st.String)
		}
		if en.Valid {
			x.EndsAt, _ = store.ParseTime(en.String)
		}
		m[x.BotID] = &x
	}
	s.mu.Lock()
	s.cache = m
	s.mu.Unlock()
	return rows.Err()
}

// Get returns the maintenance of a bot, if any.
func (s *Service) Get(botID int64) (M, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.cache[botID]
	if !ok {
		return M{}, false
	}
	return *m, true
}

// IsActive reports whether the bot is in pause or maintenance now.
func (s *Service) IsActive(_ context.Context, botID int64) bool {
	m, ok := s.Get(botID)
	return ok && m.State == Active
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return store.FormatTime(t)
}

func (s *Service) save(ctx context.Context, m *M) error {
	_, err := s.DB.W.ExecContext(ctx, `INSERT INTO maintenance (bot_id, kind, state, starts_at, ends_at, text, version, auto_end, warn_hours, warned, overdue, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (bot_id) DO UPDATE SET kind = excluded.kind, state = excluded.state, starts_at = excluded.starts_at,
		ends_at = excluded.ends_at, text = excluded.text, version = excluded.version, auto_end = excluded.auto_end,
		warn_hours = excluded.warn_hours, warned = excluded.warned, overdue = excluded.overdue`,
		m.BotID, m.Kind, m.State, nullTime(m.StartsAt), nullTime(m.EndsAt), m.Text, m.Version, m.AutoEnd, m.WarnHours, m.Warned, m.Overdue, store.Now())
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[int64]*M{}
	}
	cp := *m
	s.cache[m.BotID] = &cp
	s.mu.Unlock()
	if s.Changed != nil {
		s.Changed(m.BotID, &cp, false)
	}
	return nil
}

// ErrActive: something else is already planned or running.
var ErrActive = errors.New("maintenance: the bot already has maintenance")

// PauseBot turns on the manual pause.
func (s *Service) PauseBot(ctx context.Context, botID int64, text string) error {
	if m, ok := s.Get(botID); ok && m.State == Active {
		return ErrActive
	}
	return s.save(ctx, &M{BotID: botID, Kind: Pause, State: Active, StartsAt: time.Now().UTC(), Text: strings.TrimSpace(text)})
}

// End stops pause or maintenance; deferred updates go to the bot next.
func (s *Service) End(ctx context.Context, botID int64) error {
	if _, err := s.DB.W.ExecContext(ctx, `DELETE FROM maintenance WHERE bot_id = ?`, botID); err != nil {
		return err
	}
	s.mu.Lock()
	m := s.cache[botID]
	delete(s.cache, botID)
	s.mu.Unlock()
	if s.Changed != nil && m != nil {
		s.Changed(botID, m, true)
	}
	return nil
}

// Text renders the text for users: {start} and {end} in the bot's zone.
func (s *Service) Text(ctx context.Context, m M) string {
	t := m.Text
	if t == "" {
		t = DefaultText
		if !m.EndsAt.IsZero() {
			t = DefaultScheduledText
		}
	}
	loc := time.UTC
	if s.TZ != nil {
		loc = s.TZ(ctx, m.BotID)
	}
	return Render(t, m.StartsAt, m.EndsAt, loc, time.Now())
}

// Render puts times into {start} and {end}: "21:00", with the date when
// it is not today.
func Render(text string, start, end time.Time, loc *time.Location, now time.Time) string {
	f := func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		lt, ln := t.In(loc), now.In(loc)
		if lt.YearDay() == ln.YearDay() && lt.Year() == ln.Year() {
			return lt.Format("15:04")
		}
		return lt.Format("02.01 15:04")
	}
	return strings.NewReplacer("{start}", f(start), "{end}", f(end)).Replace(text)
}

// Check is pipeline step 9.
func (s *Service) Check(ctx context.Context, u *ingress.Update) ingress.Verdict {
	m, ok := s.Get(u.BotID)
	if !ok || m.State != Active {
		return ingress.Verdict{}
	}
	return ingress.Verdict{Stop: true, Reason: updrules.Maintenance, Text: s.Text(ctx, m)}
}

// Label is shown on the card and in the bot header.
func Label(m M, loc *time.Location) string {
	if m.Kind == Pause {
		return "пауза"
	}
	f := func(t time.Time) string { return t.In(loc).Format("15:04") }
	prefix := "техработы "
	if m.State == Planned {
		prefix = "техработы запланированы "
	}
	return prefix + f(m.StartsAt) + "–" + f(m.EndsAt)
}
