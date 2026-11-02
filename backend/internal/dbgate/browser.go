// Package dbgate gives the panel access to bot databases (bot.db, SQLite).
// Without the SDK the bot owns the file; the core reads it read-only next to
// the bot (WAL lets readers and the writer work together). Writes from the
// console use a separate connection and wait for their turn.
package dbgate

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrNoDB means the bot has not created its database yet.
var ErrNoDB = errors.New("dbgate: the bot has no database yet")

// QueryTimeout bounds console queries.
var QueryTimeout = 30 * time.Second

// MaxRows bounds results shown in the console.
const MaxRows = 1000

// Browser opens bot databases.
type Browser struct {
	// Path returns bot.db of a bot.
	Path func(botID int64) string
	// UID is the bot's uid: its database is opened with the bot's
	// filesystem identity, so a link the bot puts in place of bot.db
	// cannot reach a file of the core. nil or -1: the current identity.
	UID func(botID int64) int
}

func (b *Browser) uid(botID int64) int {
	if b.UID == nil {
		return -1
	}
	return b.UID(botID)
}

func dsn(path string, write bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	// The bot writes the schema: its views and triggers run no functions
	// that are not harmless when the core reads the file.
	q.Add("_pragma", "trusted_schema(0)")
	if !write {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	}
	return sqliteURI(path, q)
}

// readOnly takes a connection that cannot reach past the bot's database:
// query_only stops writes to it, but ATTACH and VACUUM INTO would still
// create files anywhere the process may write. No attached databases at all.
func readOnly(ctx context.Context, db *sql.DB) (*sql.Conn, error) {
	c, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := sqlite.Limit(c, sqlite3.SQLITE_LIMIT_ATTACHED, 0); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (b *Browser) open(botID int64, write bool) (*sql.DB, error) {
	p := b.Path(botID)
	if _, err := os.Stat(p); err != nil {
		return nil, ErrNoDB
	}
	db, err := sql.Open("sqlite", dsn(p, write))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// Table describes one table.
type Table struct {
	Name   string `cbor:"1,keyasint" json:"name"`
	Type   string `cbor:"2,keyasint" json:"type"` // table | view
	SQL    string `cbor:"3,keyasint" json:"sql"`
	Rows   int64  `cbor:"4,keyasint" json:"rows"`
	Schema []Col  `cbor:"5,keyasint" json:"columns"`
}

// Col is a column.
type Col struct {
	Name    string `cbor:"1,keyasint" json:"name"`
	Type    string `cbor:"2,keyasint" json:"type"`
	NotNull bool   `cbor:"3,keyasint" json:"not_null"`
	PK      bool   `cbor:"4,keyasint" json:"pk"`
}

// Info is the database overview.
type Info struct {
	Size    int64   `cbor:"1,keyasint" json:"size"`
	WALSize int64   `cbor:"2,keyasint" json:"wal_size"`
	Tables  []Table `cbor:"3,keyasint" json:"tables"`
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// Sizes returns bot.db and WAL sizes (for counters).
func (b *Browser) Sizes(botID int64) (db, wal int64) {
	p := b.Path(botID)
	if fi, err := os.Lstat(p); err == nil {
		db = fi.Size()
	}
	if fi, err := os.Lstat(p + "-wal"); err == nil {
		wal = fi.Size()
	}
	return
}

// Tables lists tables and views with schema and row counts.
func (b *Browser) Tables(ctx context.Context, botID int64) (info Info, err error) {
	err = withIdentity(b.uid(botID), func() error { info, err = b.tables(ctx, botID); return err })
	return info, err
}

// Rows returns a page of a table.
func (b *Browser) Rows(ctx context.Context, botID int64, table string, limit, offset int) (r Result, err error) {
	err = withIdentity(b.uid(botID), func() error { r, err = b.rows(ctx, botID, table, limit, offset); return err })
	return r, err
}

// Query runs console SQL with the bot's identity (see query).
func (b *Browser) Query(ctx context.Context, botID int64, query string, write bool) (r Result, err error) {
	err = withIdentity(b.uid(botID), func() error { r, err = b.query(ctx, botID, query, write); return err })
	return r, err
}

// CSV exports a table or a read-only query with the bot's identity.
func (b *Browser) CSV(ctx context.Context, botID int64, table, query string, w io.Writer) error {
	return withIdentity(b.uid(botID), func() error { return b.csv(ctx, botID, table, query, w) })
}

func (b *Browser) tables(ctx context.Context, botID int64) (Info, error) {
	var info Info
	info.Size, info.WALSize = b.Sizes(botID)
	db, err := b.open(botID, false)
	if err != nil {
		return info, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT name, type, COALESCE(sql, '') FROM sqlite_schema WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return info, err
	}
	for rows.Next() {
		var t Table
		if err := rows.Scan(&t.Name, &t.Type, &t.SQL); err != nil {
			rows.Close()
			return info, err
		}
		info.Tables = append(info.Tables, t)
	}
	rows.Close()
	for i := range info.Tables {
		t := &info.Tables[i]
		db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdent(t.Name)).Scan(&t.Rows)
		cr, err := db.QueryContext(ctx, `SELECT name, type, "notnull", pk FROM pragma_table_info(?)`, t.Name)
		if err != nil {
			continue
		}
		for cr.Next() {
			var c Col
			var pk int
			cr.Scan(&c.Name, &c.Type, &c.NotNull, &pk)
			c.PK = pk > 0
			t.Schema = append(t.Schema, c)
		}
		cr.Close()
	}
	if info.Tables == nil {
		info.Tables = []Table{}
	}
	return info, nil
}

// Result is a query result.
type Result struct {
	Columns   []string `cbor:"1,keyasint" json:"columns"`
	Types     []string `cbor:"2,keyasint" json:"types"`
	Rows      [][]any  `cbor:"3,keyasint" json:"rows"`
	Truncated bool     `cbor:"4,keyasint" json:"truncated,omitempty"`
	Affected  int64    `cbor:"5,keyasint" json:"affected,omitempty"`
	Millis    float64  `cbor:"6,keyasint" json:"ms"`
}

// value makes a SQLite value printable: bytes as x'…' (cut at 64 bytes).
func value(v any) any {
	switch x := v.(type) {
	case []byte:
		s := hex.EncodeToString(x[:min(len(x), 64)])
		if len(x) > 64 {
			s += "…"
		}
		return "x'" + s + "'"
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	}
	return v
}

func collect(rows *sql.Rows, limit int) (Result, error) {
	var r Result
	cols, _ := rows.Columns()
	types, _ := rows.ColumnTypes()
	r.Columns = cols
	for _, t := range types {
		r.Types = append(r.Types, t.DatabaseTypeName())
	}
	r.Rows = [][]any{}
	for rows.Next() {
		if len(r.Rows) >= limit {
			r.Truncated = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return r, err
		}
		for i := range vals {
			vals[i] = value(vals[i])
		}
		r.Rows = append(r.Rows, vals)
	}
	return r, rows.Err()
}

// Rows returns a page of a table.
func (b *Browser) rows(ctx context.Context, botID int64, table string, limit, offset int) (Result, error) {
	db, err := b.open(botID, false)
	if err != nil {
		return Result{}, err
	}
	defer db.Close()
	if limit <= 0 || limit > MaxRows {
		limit = 100
	}
	var n int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name = ? AND type IN ('table', 'view')`, table).Scan(&n)
	if n == 0 {
		return Result{}, fmt.Errorf("dbgate: no table %q", table)
	}
	rows, err := db.QueryContext(ctx, `SELECT * FROM `+quoteIdent(table)+` LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()
	return collect(rows, limit)
}

// Query runs console SQL. Read-only by default: the connection itself is
// read-only (query_only), the text is not inspected. With write it uses a
// separate read-write connection that waits for the bot's writes.
func (b *Browser) query(ctx context.Context, botID int64, query string, write bool) (Result, error) {
	db, err := b.open(botID, write)
	if err != nil {
		return Result{}, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	start := time.Now()
	q := strings.TrimSpace(query)
	head := strings.ToUpper(strings.SplitN(q, " ", 2)[0])
	if write && head != "SELECT" && head != "WITH" && head != "PRAGMA" && head != "EXPLAIN" {
		res, err := db.ExecContext(ctx, q)
		if err != nil {
			return Result{}, err
		}
		n, _ := res.RowsAffected()
		return Result{Columns: []string{}, Rows: [][]any{}, Affected: n, Millis: ms(start)}, nil
	}
	var querier interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	} = db
	if !write {
		c, err := readOnly(ctx, db)
		if err != nil {
			return Result{}, err
		}
		defer c.Close()
		querier = c
	}
	rows, err := querier.QueryContext(ctx, q)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()
	r, err := collect(rows, MaxRows)
	r.Millis = ms(start)
	return r, err
}

func ms(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

// CSV writes a table (or a read-only query) as CSV.
func (b *Browser) csv(ctx context.Context, botID int64, table, query string, w io.Writer) error {
	db, err := b.open(botID, false)
	if err != nil {
		return err
	}
	defer db.Close()
	q := query
	if table != "" {
		q = `SELECT * FROM ` + quoteIdent(table)
	}
	c, err := readOnly(ctx, db)
	if err != nil {
		return err
	}
	defer c.Close()
	rows, err := c.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	cw := csv.NewWriter(w)
	cols, _ := rows.Columns()
	cw.Write(cols)
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	rec := make([]string, len(cols))
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for i, v := range vals {
			switch x := v.(type) {
			case nil:
				rec[i] = ""
			case []byte:
				rec[i] = "x'" + hex.EncodeToString(x) + "'"
			case int64:
				rec[i] = strconv.FormatInt(x, 10)
			case float64:
				rec[i] = strconv.FormatFloat(x, 'g', -1, 64)
			default:
				rec[i] = fmt.Sprint(x)
			}
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return err
	}
	return rows.Err()
}
