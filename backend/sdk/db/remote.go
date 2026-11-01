package db

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk/db/protocol"
	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
)

type remoteConn struct {
	session *wire.Conn
	id      string
	closed  atomic.Bool
	release func()
}

func (c *remoteConn) request(ctx context.Context, method string, in protocol.Request, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.closed.Load() {
		return driver.ErrBadConn
	}
	select {
	case <-c.session.Done():
		return driver.ErrBadConn
	default:
	}
	in.Conn = c.id
	err := c.session.Call(ctx, method, in, out)
	var we *wire.Error
	if errors.As(err, &we) && we.Code == "db_connection_gone" {
		return driver.ErrBadConn
	}
	// Do not turn an in-flight disconnect into ErrBadConn: database/sql
	// would retry a write that the core may already have committed.
	return err
}

func (c *remoteConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	defer c.release()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.session.Call(ctx, "db.close", protocol.Request{Conn: c.id}, nil)
}

func (c *remoteConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}
func (c *remoteConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var p protocol.Prepared
	if err := c.request(ctx, "db.prepare", protocol.Request{SQL: query}, &p); err != nil {
		return nil, err
	}
	return &stmt{conn: c, id: p.ID, n: p.NumInput}, nil
}

func (c *remoteConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *remoteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.request(ctx, "db.begin", protocol.Request{Isolation: int(opts.Isolation), ReadOnly: opts.ReadOnly}, nil); err != nil {
		return nil, err
	}
	return &tx{conn: c}, nil
}

func arguments(args []driver.NamedValue) ([]protocol.Arg, error) {
	out := make([]protocol.Arg, len(args))
	for i, a := range args {
		v, err := protocol.Encode(a.Value)
		if err != nil {
			return nil, err
		}
		out[i] = protocol.Arg{Name: a.Name, Ordinal: a.Ordinal, Value: v}
	}
	return out, nil
}

func (c *remoteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.exec(ctx, protocol.Request{SQL: query}, args)
}
func (c *remoteConn) exec(ctx context.Context, req protocol.Request, args []driver.NamedValue) (driver.Result, error) {
	var out protocol.Result
	a, err := arguments(args)
	if err != nil {
		return nil, err
	}
	req.Args = a
	if err := c.request(ctx, "db.exec", req, &out); err != nil {
		return nil, err
	}
	return result(out), nil
}

func (c *remoteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.query(ctx, protocol.Request{SQL: query}, args)
}
func (c *remoteConn) query(ctx context.Context, req protocol.Request, args []driver.NamedValue) (driver.Rows, error) {
	a, err := arguments(args)
	if err != nil {
		return nil, err
	}
	req.Args = a
	var b protocol.Batch
	if err := c.request(ctx, "db.query", req, &b); err != nil {
		return nil, err
	}
	return &rows{conn: c, ctx: ctx, batch: b}, nil
}

func (c *remoteConn) Ping(ctx context.Context) error {
	return c.request(ctx, "db.ping", protocol.Request{}, nil)
}
func (c *remoteConn) IsValid() bool {
	select {
	case <-c.session.Done():
		return false
	default:
		return !c.closed.Load()
	}
}

type result protocol.Result

func (r result) LastInsertId() (int64, error) { return r.LastID, nil }
func (r result) RowsAffected() (int64, error) { return r.Affected, nil }

type tx struct{ conn *remoteConn }

func (t *tx) Commit() error {
	return t.conn.request(context.Background(), "db.commit", protocol.Request{}, nil)
}
func (t *tx) Rollback() error {
	return t.conn.request(context.Background(), "db.rollback", protocol.Request{}, nil)
}

type stmt struct {
	conn *remoteConn
	id   string
	n    int
}

func (s *stmt) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.conn.request(ctx, "db.stmt_close", protocol.Request{Stmt: s.id}, nil)
}
func (s *stmt) NumInput() int { return s.n }
func named(args []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	for i, v := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return out
}
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), named(args))
}
func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), named(args))
}
func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.conn.exec(ctx, protocol.Request{Stmt: s.id}, args)
}
func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.conn.query(ctx, protocol.Request{Stmt: s.id}, args)
}

type rows struct {
	conn   *remoteConn
	ctx    context.Context
	batch  protocol.Batch
	index  int
	closed bool
}

func (r *rows) Columns() []string { return r.batch.Columns }
func (r *rows) ColumnTypeDatabaseTypeName(i int) string {
	if i < len(r.batch.Types) {
		return r.batch.Types[i]
	}
	return ""
}
func (r *rows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.batch.Done || r.batch.Cursor == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.conn.request(ctx, "db.rows_close", protocol.Request{Cursor: r.batch.Cursor}, nil)
}
func (r *rows) Next(dest []driver.Value) error {
	if r.closed {
		return io.EOF
	}
	for r.index >= len(r.batch.Rows) {
		if r.batch.Done {
			return io.EOF
		}
		columns, types := r.batch.Columns, r.batch.Types
		var next protocol.Batch
		if err := r.conn.request(r.ctx, "db.next", protocol.Request{Cursor: r.batch.Cursor}, &next); err != nil {
			return err
		}
		r.batch = next
		r.batch.Columns = columns
		r.batch.Types = types
		r.index = 0
	}
	for i, v := range r.batch.Rows[r.index] {
		value, err := v.Decode()
		if err != nil {
			return err
		}
		dest[i] = value
	}
	r.index++
	return nil
}

var (
	_ driver.DriverContext      = (*Driver)(nil)
	_ driver.ConnPrepareContext = (*remoteConn)(nil)
	_ driver.ConnBeginTx        = (*remoteConn)(nil)
	_ driver.ExecerContext      = (*remoteConn)(nil)
	_ driver.QueryerContext     = (*remoteConn)(nil)
	_ driver.Validator          = (*remoteConn)(nil)
	_ driver.StmtExecContext    = (*stmt)(nil)
	_ driver.StmtQueryContext   = (*stmt)(nil)
)
