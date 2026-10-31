// Package journal records every changing operation of the core with its
// source: the panel (key name), the CLI over SSH, autorollback or the
// scheduler. The panel pulls the journal into the common audit log, so
// actions over SSH are visible there too.
package journal

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Sources of commands.
const (
	SourceUI           = "ui"
	SourceMCP          = "mcp"
	SourceCLI          = "cli"
	SourceAutorollback = "autorollback"
	SourceSchedule     = "schedule"
	SourceCore         = "core"
)

// Actor is who asked for an operation. Requests from the panel carry it.
type Actor struct {
	Source string `cbor:"1,keyasint" json:"source"`
	Name   string `cbor:"2,keyasint" json:"name,omitempty"`
	IP     string `cbor:"3,keyasint" json:"ip,omitempty"`
}

// Entry is one journal row.
type Entry struct {
	ID      int64           `cbor:"1,keyasint" json:"id"`
	Time    time.Time       `cbor:"2,keyasint" json:"ts"`
	Actor   Actor           `cbor:"3,keyasint" json:"actor"`
	Command string          `cbor:"4,keyasint" json:"command"`
	BotID   int64           `cbor:"5,keyasint" json:"bot_id,omitempty"`
	Args    json.RawMessage `cbor:"6,keyasint" json:"args,omitempty"`
	Result  string          `cbor:"7,keyasint" json:"result"`
}

// MethodSince is the socket method the panel pulls with.
const MethodSince = "journal.since"

// Journal writes to core.db.
type Journal struct {
	db  *store.DB
	log *slog.Logger
}

// New registers journal.since on mux.
func New(db *store.DB, mux *wire.Mux, log *slog.Logger) *Journal {
	if log == nil {
		log = slog.Default()
	}
	j := &Journal{db: db, log: log}
	if mux != nil {
		mux.Handle(MethodSince, wire.Func(func(ctx context.Context, _ *wire.Conn, after int64) ([]Entry, error) {
			return j.Since(ctx, after, 1000)
		}))
	}
	return j
}

// Do runs fn and records it with its result. Secrets never reach args:
// callers pass only names.
func (j *Journal) Do(ctx context.Context, a Actor, command string, botID int64, args any, fn func() error) error {
	err := fn()
	result := "ok"
	if err != nil {
		result = "error: " + err.Error()
	}
	j.Record(ctx, a, command, botID, args, result)
	return err
}

// Record writes one entry.
func (j *Journal) Record(ctx context.Context, a Actor, command string, botID int64, args any, result string) {
	if a.Source == "" {
		a.Source = SourceCore
	}
	raw := []byte("{}")
	if args != nil {
		if b, err := json.Marshal(args); err == nil {
			raw = b
		}
	}
	_, err := j.db.W.ExecContext(context.WithoutCancel(ctx), `INSERT INTO journal (ts, source, actor, ip, command, bot_id, args, result) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		store.Now(), a.Source, a.Name, a.IP, command, botID, string(raw), result)
	if err != nil {
		j.log.Error("journal write failed", "command", command, "err", err)
	}
}

// Since returns entries with id > after, oldest first.
func (j *Journal) Since(ctx context.Context, after int64, limit int) ([]Entry, error) {
	rows, err := j.db.R.QueryContext(ctx, `SELECT id, ts, source, actor, ip, command, bot_id, args, result FROM journal WHERE id > ? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var ts, args string
		if err := rows.Scan(&e.ID, &ts, &e.Actor.Source, &e.Actor.Name, &e.Actor.IP, &e.Command, &e.BotID, &args, &e.Result); err != nil {
			return nil, err
		}
		e.Time, _ = store.ParseTime(ts)
		e.Args = json.RawMessage(args)
		out = append(out, e)
	}
	return out, rows.Err()
}
