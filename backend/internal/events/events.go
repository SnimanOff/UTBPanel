// Package events carries core events to the panel.
//
// The core keeps events in a ring in memory and in core.db for 7 days. The
// panel subscribes over the socket and after each reconnect fetches what it
// missed by the last id, so an event reaches the panel even if the panel was
// down when it happened.
package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Event types.
const (
	BotStarted      = "bot_started"
	BotStopped      = "bot_stopped"
	BotCrashed      = "bot_crashed"
	BotCrashLoop    = "bot_crash_loop"
	BotRestarted    = "bot_restarted"
	LimitWarning    = "limit_warning"
	LimitReached    = "limit_reached"
	WebhookHijacked = "webhook_hijacked"
	WebhookError    = "webhook_error"
	NewDevice       = "new_device"
	Test            = "test"
)

// Severities.
const (
	Info  = "info"
	Warn  = "warn"
	Error = "error"
)

// Event is one thing that happened in the core.
type Event struct {
	ID       int64          `cbor:"1,keyasint" json:"id"`
	Time     time.Time      `cbor:"2,keyasint" json:"ts"`
	Type     string         `cbor:"3,keyasint" json:"type"`
	BotID    int64          `cbor:"4,keyasint,omitempty" json:"bot_id,omitempty"`
	Severity string         `cbor:"5,keyasint" json:"severity"`
	Data     map[string]any `cbor:"6,keyasint,omitempty" json:"data,omitempty"`
}

// Socket names.
const (
	MethodSince = "events.since"
	EventPush   = "event"
)

const (
	ringSize  = 1024
	retention = 7 * 24 * time.Hour
)

// Bus is the core side.
type Bus struct {
	db  *store.DB
	log *slog.Logger
	srv *wire.Server

	mu   sync.Mutex
	ring []Event
	subs []func(Event)
}

// NewBus registers events.since on mux and returns the bus.
func NewBus(db *store.DB, srv *wire.Server, mux *wire.Mux, log *slog.Logger) *Bus {
	if log == nil {
		log = slog.Default()
	}
	b := &Bus{db: db, srv: srv, log: log}
	mux.Handle(MethodSince, wire.Func(func(ctx wire.Context, _ *wire.Conn, after int64) ([]Event, error) {
		return b.Since(ctx, after, 1000)
	}))
	return b
}

// Subscribe adds an in-process listener (for example, the edge settings sync).
func (b *Bus) Subscribe(fn func(Event)) {
	b.mu.Lock()
	b.subs = append(b.subs, fn)
	b.mu.Unlock()
}

// Emit stores the event and pushes it to connected panels.
func (b *Bus) Emit(typ string, botID int64, severity string, data map[string]any) Event {
	ev := Event{Time: time.Now().UTC(), Type: typ, BotID: botID, Severity: severity, Data: data}
	raw, _ := json.Marshal(data)
	if data == nil {
		raw = []byte("{}")
	}
	err := store.Tx(context.Background(), b.db.W, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO events (ts, type, bot_id, severity, data) VALUES (?, ?, ?, ?, ?)`,
			store.FormatTime(ev.Time), typ, botID, severity, string(raw))
		if err != nil {
			return err
		}
		ev.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		b.log.Error("event not stored", "type", typ, "bot", botID, "err", err)
	}
	b.log.Info("event", "type", typ, "bot", botID, "severity", severity, "data", data)
	b.mu.Lock()
	b.ring = append(b.ring, ev)
	if len(b.ring) > ringSize {
		b.ring = b.ring[len(b.ring)-ringSize:]
	}
	subs := slices.Clone(b.subs)
	b.mu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
	if b.srv != nil {
		for _, c := range b.srv.Conns() {
			if c.Proto() == wire.ProtoCorePanel {
				c.Notify(EventPush, ev)
			}
		}
	}
	return ev
}

// Since returns events with id > after, oldest first.
func (b *Bus) Since(ctx context.Context, after int64, limit int) ([]Event, error) {
	b.mu.Lock()
	if len(b.ring) > 0 && b.ring[0].ID <= after+1 {
		var out []Event
		for _, ev := range b.ring {
			if ev.ID > after {
				out = append(out, ev)
				if len(out) == limit {
					break
				}
			}
		}
		b.mu.Unlock()
		return out, nil
	}
	b.mu.Unlock()
	return query(ctx, b.db.R, `SELECT id, ts, type, bot_id, severity, data FROM events WHERE id > ? ORDER BY id LIMIT ?`, after, limit)
}

// Prune deletes events older than 7 days; run it periodically.
func (b *Bus) Prune(ctx context.Context) error {
	_, err := b.db.W.ExecContext(ctx, `DELETE FROM events WHERE ts < ?`, store.FormatTime(time.Now().Add(-retention)))
	return err
}

// Run prunes old events every hour until ctx ends.
func (b *Bus) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := b.Prune(ctx); err != nil && ctx.Err() == nil {
			b.log.Warn("events prune failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func query(ctx context.Context, db *sql.DB, q string, args ...any) ([]Event, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var ev Event
		var ts, data string
		if err := rows.Scan(&ev.ID, &ts, &ev.Type, &ev.BotID, &ev.Severity, &data); err != nil {
			return nil, err
		}
		ev.Time, _ = store.ParseTime(ts)
		json.Unmarshal([]byte(data), &ev.Data)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// Sink is the panel side: it copies core events into panel.db.
type Sink struct {
	db  *store.DB
	log *slog.Logger

	mu   sync.Mutex
	subs []func(Event)
}

// NewSink registers the push handler on the panel's core mux.
func NewSink(db *store.DB, mux *wire.Mux, log *slog.Logger) *Sink {
	if log == nil {
		log = slog.Default()
	}
	s := &Sink{db: db, log: log}
	mux.OnEvent(EventPush, func(_ *wire.Conn, _ string, body wire.Raw) {
		var ev Event
		if err := wire.Unmarshal(body, &ev); err == nil {
			s.save([]Event{ev})
		}
	})
	return s
}

// Subscribe adds a listener for new events (notifications, SSE).
func (s *Sink) Subscribe(fn func(Event)) {
	s.mu.Lock()
	s.subs = append(s.subs, fn)
	s.mu.Unlock()
}

// LastID returns the newest stored core event id.
func (s *Sink) LastID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.R.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM core_events`).Scan(&id)
	return id, err
}

// CatchUp fetches events missed while the panel was away; call it on every
// connect to the core.
func (s *Sink) CatchUp(ctx context.Context, c *wire.Conn) error {
	for {
		after, err := s.LastID(ctx)
		if err != nil {
			return err
		}
		var evs []Event
		if err := c.Call(ctx, MethodSince, after, &evs); err != nil {
			return err
		}
		if len(evs) == 0 {
			return nil
		}
		s.save(evs)
		if len(evs) < 1000 {
			return nil
		}
	}
}

func (s *Sink) save(evs []Event) {
	var fresh []Event
	err := s.db.Tx(context.Background(), func(tx *sql.Tx) error {
		for _, ev := range evs {
			raw, _ := json.Marshal(ev.Data)
			if ev.Data == nil {
				raw = []byte("{}")
			}
			res, err := tx.Exec(`INSERT OR IGNORE INTO core_events (id, ts, type, bot_id, severity, data) VALUES (?, ?, ?, ?, ?, ?)`,
				ev.ID, store.FormatTime(ev.Time), ev.Type, ev.BotID, ev.Severity, string(raw))
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				fresh = append(fresh, ev)
			}
		}
		return nil
	})
	if err != nil {
		s.log.Error("core events not saved", "err", err)
		return
	}
	s.mu.Lock()
	subs := slices.Clone(s.subs)
	s.mu.Unlock()
	for _, ev := range fresh {
		for _, fn := range subs {
			fn(ev)
		}
	}
}

// Recent returns stored events, newest first.
func (s *Sink) Recent(ctx context.Context, botID int64, limit int) ([]Event, error) {
	if botID > 0 {
		return query(ctx, s.db.R, `SELECT id, ts, type, bot_id, severity, data FROM core_events WHERE bot_id = ? ORDER BY id DESC LIMIT ?`, botID, limit)
	}
	return query(ctx, s.db.R, `SELECT id, ts, type, bot_id, severity, data FROM core_events ORDER BY id DESC LIMIT ?`, limit)
}
