// Package store opens SQLite databases and applies migrations.
//
// Every database gets one write connection and a separate read-only pool:
// SQLite allows a single writer, so funnelling writes through one
// connection avoids "database is locked".
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB is a SQLite database with separate write and read handles.
type DB struct {
	W    *sql.DB // single connection for writes
	R    *sql.DB // read-only pool
	Path string
}

// ErrTooNew means the database was migrated by a newer program version.
var ErrTooNew = errors.New("database is newer than this program")

func dsn(file string, readOnly bool) string {
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "busy_timeout(5000)", "foreign_keys(ON)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	}
	q.Set("_txlock", "immediate")
	return "file:" + file + "?" + q.Encode()
}

// Open opens the database at file and applies the migrations found in
// migrations (files named 0001_name.sql, applied in order).
func Open(ctx context.Context, file string, migrations fs.FS) (*DB, error) {
	w, err := sql.Open("sqlite", dsn(file, false))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetConnMaxIdleTime(0)
	if err := w.PingContext(ctx); err != nil {
		w.Close()
		return nil, fmt.Errorf("open %s: %w", file, err)
	}
	if migrations != nil {
		if err := Migrate(ctx, w, migrations); err != nil {
			w.Close()
			return nil, fmt.Errorf("migrate %s: %w", file, err)
		}
	}
	r, err := sql.Open("sqlite", dsn(file, true))
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(max(4, runtime.NumCPU()))
	return &DB{W: w, R: r, Path: file}, nil
}

// Close closes both handles.
func (db *DB) Close() error {
	return errors.Join(db.R.Close(), db.W.Close())
}

// Tx runs fn in a write transaction, committing if fn returns nil.
func (db *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	return Tx(ctx, db.W, fn)
}

// Tx runs fn in a transaction on any handle.
func Tx(ctx context.Context, h *sql.DB, fn func(*sql.Tx) error) (err error) {
	tx, err := h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
		if err != nil {
			tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

type migration struct {
	version int
	name    string
	sql     string
}

func readMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		num, _, _ := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %s: name must start with a number", e.Name())
		}
		b, err := fs.ReadFile(fsys, path.Clean(e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, migration{v, e.Name(), string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := range out {
		if out[i].version != i+1 {
			return nil, fmt.Errorf("migration %s: expected number %d", out[i].name, i+1)
		}
	}
	return out, nil
}

// Migrate applies pending migrations in one transaction.
func Migrate(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	ms, err := readMigrations(fsys)
	if err != nil {
		return err
	}
	return Tx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
			return err
		}
		var cur int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&cur); err != nil {
			return err
		}
		if cur > len(ms) {
			return fmt.Errorf("%w: schema %d, supported %d", ErrTooNew, cur, len(ms))
		}
		for _, m := range ms[cur:] {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return fmt.Errorf("%s: %w", m.name, err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
				m.version, m.name, Now()); err != nil {
				return err
			}
		}
		return nil
	})
}

// Now returns the current UTC time in the text format used in all tables.
func Now() string { return FormatTime(time.Now()) }

// FormatTime formats t as UTC RFC 3339 with milliseconds.
func FormatTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// ParseTime parses a value written by FormatTime.
func ParseTime(s string) (time.Time, error) { return time.Parse("2006-01-02T15:04:05.000Z", s) }
