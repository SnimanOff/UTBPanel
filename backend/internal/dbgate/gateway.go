package dbgate

import (
	"context"
	"crypto/rand"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk/db/protocol"
	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
	"modernc.org/sqlite"
)

// Gateway executes SDK SQL against a fixed bot.db, one physical connection
// per SQL connection. No client-supplied filename reaches SQLite.
type Gateway struct {
	Path          func(int64) string
	UID           func(int64) int
	Timeout       time.Duration
	SlowThreshold time.Duration
	Observe       func(int64, string, time.Duration, error)
	mu            sync.Mutex
	sessions      map[*wire.Conn]*dbSession
	statsMu       sync.Mutex
	stats         map[int64]*QueryStats
}

type dbSession struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	conns  map[string]*sqlConn
	closed bool
}

type preparedSQL struct {
	stmt driver.Stmt
	text string
}
type sqlConn struct {
	mu       sync.Mutex
	conn     driver.Conn
	uid      int
	closed   bool
	stmts    map[string]preparedSQL
	cursors  map[string]*sqlCursor
	tx       driver.Tx
	txCancel context.CancelFunc
	readOnly bool
}

type sqlCursor struct {
	rows   driver.Rows
	ctx    context.Context
	cancel context.CancelFunc
	finish func(error)
}

func resourceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (g *Gateway) session(c *wire.Conn, create bool) *dbSession {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sessions == nil {
		g.sessions = map[*wire.Conn]*dbSession{}
	}
	s := g.sessions[c]
	if s == nil && create {
		ctx, cancel := context.WithCancel(context.Background())
		s = &dbSession{ctx: ctx, cancel: cancel, conns: map[string]*sqlConn{}}
		g.sessions[c] = s
		go func() { <-c.Done(); g.Disconnect(c) }()
	}
	return s
}

func (g *Gateway) Disconnect(c *wire.Conn) {
	g.mu.Lock()
	s := g.sessions[c]
	delete(g.sessions, c)
	g.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	s.cancel()
	conns := s.conns
	s.conns = map[string]*sqlConn{}
	s.mu.Unlock()
	for _, e := range conns {
		e.mu.Lock()
		withIdentity(e.uid, func() error { e.close(); return nil })
		e.mu.Unlock()
	}
}

func (e *sqlConn) close() {
	if e.closed {
		return
	}
	e.closed = true
	for id, c := range e.cursors {
		delete(e.cursors, id)
		c.rows.Close()
		c.cancel()
		c.finish(context.Canceled)
	}
	for _, p := range e.stmts {
		p.stmt.Close()
	}
	if e.tx != nil {
		e.tx.Rollback()
	}
	if e.txCancel != nil {
		e.txCancel()
		e.txCancel = nil
	}
	e.conn.Close()
}

func (g *Gateway) timeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return 30 * time.Second
}

func sqlPolicy(e *sqlConn) sqlite.AuthorizerFn {
	return func(action sqlite.AuthorizerActionCode, a, b, _, _ string) sqlite.AuthorizerReturnCode {
		switch action {
		case sqlite.AuthAttach, sqlite.AuthDetach:
			return sqlite.AuthorizerDeny
		case sqlite.AuthFunction:
			if strings.EqualFold(b, "load_extension") {
				return sqlite.AuthorizerDeny
			}
		case sqlite.AuthPragma:
			if strings.EqualFold(a, "temp_store_directory") || strings.EqualFold(a, "data_store_directory") {
				return sqlite.AuthorizerDeny
			}
		}
		if e.readOnly {
			switch action {
			case sqlite.AuthRead, sqlite.AuthSelect, sqlite.AuthFunction, sqlite.AuthTransaction, sqlite.AuthRecursive, sqlite.AuthSavepoint:
			default:
				return sqlite.AuthorizerDeny
			}
		}
		return sqlite.AuthorizerOK
	}
}

// Register installs only SDK DB operations on a per-bot router.
func (g *Gateway) Register(mux *wire.Mux, botID int64) {
	mux.Handle("db.open", wire.Func(func(ctx context.Context, c *wire.Conn, in protocol.Open) (protocol.Opened, error) {
		if in.Version != protocol.Version {
			return protocol.Opened{}, wire.Errorf("incompatible", "database protocol %d is unsupported", in.Version)
		}
		s := g.session(c, true)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			return protocol.Opened{}, wire.ErrClosed
		}
		if len(s.conns) >= 64 {
			return protocol.Opened{}, fmt.Errorf("dbgate: connection limit reached")
		}
		uid := -1
		if g.UID != nil {
			uid = g.UID(botID)
		}
		e := &sqlConn{uid: uid, stmts: map[string]preparedSQL{}, cursors: map[string]*sqlCursor{}}
		err := withIdentity(uid, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			var err error
			e.conn, err = (&sqlite.Driver{}).Open("file:" + g.Path(botID) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
			if err != nil {
				return err
			}
			if err := e.conn.(sqlite.AuthorizerRegisterer).RegisterAuthorizer(sqlPolicy(e)); err != nil {
				e.conn.Close()
				return err
			}
			return nil
		})
		if err != nil {
			return protocol.Opened{}, err
		}
		id := resourceID()
		s.conns[id] = e
		return protocol.Opened{ID: id, Version: protocol.Version}, nil
	}))
	for _, method := range []string{"ping", "close", "prepare", "stmt_close", "exec", "query", "next", "rows_close", "begin", "commit", "rollback"} {
		mux.Handle("db."+method, wire.Func(func(ctx context.Context, c *wire.Conn, in protocol.Request) (any, error) {
			return g.handle(ctx, c, botID, method, in)
		}))
	}
}

func (g *Gateway) handle(ctx context.Context, c *wire.Conn, botID int64, method string, in protocol.Request) (out any, err error) {
	s := g.session(c, false)
	if s == nil {
		return nil, wire.Errorf("db_connection_gone", "database session is closed")
	}
	s.mu.Lock()
	e := s.conns[in.Conn]
	s.mu.Unlock()
	if e == nil {
		return nil, wire.Errorf("db_connection_gone", "database connection is closed")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, wire.Errorf("db_connection_gone", "database connection is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	err = withIdentity(e.uid, func() error {
		var inner error
		out, inner = g.execute(ctx, s, e, botID, method, in)
		return inner
	})
	return out, err
}

func decodeArgs(in []protocol.Arg) ([]driver.NamedValue, error) {
	args := make([]driver.NamedValue, len(in))
	for i, a := range in {
		v, err := a.Value.Decode()
		if err != nil {
			return nil, err
		}
		args[i] = driver.NamedValue{Name: a.Name, Ordinal: a.Ordinal, Value: v}
	}
	return args, nil
}

func (g *Gateway) execute(ctx context.Context, s *dbSession, e *sqlConn, botID int64, method string, in protocol.Request) (any, error) {
	switch method {
	case "close":
		s.mu.Lock()
		delete(s.conns, in.Conn)
		s.mu.Unlock()
		e.close()
		return nil, nil
	case "ping":
		return nil, e.conn.(driver.Pinger).Ping(ctx)
	case "prepare":
		if len(e.stmts) >= 128 {
			return nil, fmt.Errorf("dbgate: prepared statement limit reached")
		}
		p, err := e.conn.(driver.ConnPrepareContext).PrepareContext(ctx, in.SQL)
		if err != nil {
			return nil, err
		}
		id := resourceID()
		e.stmts[id] = preparedSQL{stmt: p, text: in.SQL}
		return protocol.Prepared{ID: id, NumInput: p.NumInput()}, nil
	case "stmt_close":
		if p, ok := e.stmts[in.Stmt]; ok {
			delete(e.stmts, in.Stmt)
			return nil, p.stmt.Close()
		}
		return nil, nil
	case "begin":
		if e.tx != nil {
			return nil, fmt.Errorf("dbgate: transaction already active")
		}
		// The driver retains BeginTx's context until Commit. An RPC handler
		// context ends as soon as its response is sent, not at transaction end.
		txCtx, cancel := context.WithCancel(s.ctx)
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		tx, err := e.conn.(driver.ConnBeginTx).BeginTx(txCtx, driver.TxOptions{Isolation: driver.IsolationLevel(in.Isolation), ReadOnly: in.ReadOnly})
		if err == nil && ctx.Err() != nil {
			tx.Rollback()
			cancel()
			return nil, ctx.Err()
		}
		if err == nil {
			e.tx = tx
			e.txCancel = cancel
			e.readOnly = in.ReadOnly
			err = e.conn.(sqlite.AuthorizerRegisterer).RegisterAuthorizer(sqlPolicy(e))
			if err != nil {
				tx.Rollback()
				e.tx = nil
				e.readOnly = false
			}
		}
		if err != nil {
			cancel()
		}
		return nil, err
	case "commit", "rollback":
		if e.tx == nil {
			return nil, fmt.Errorf("dbgate: no active transaction")
		}
		var err error
		if method == "commit" {
			err = e.tx.Commit()
		} else {
			err = e.tx.Rollback()
		}
		e.tx = nil
		if e.txCancel != nil {
			e.txCancel()
			e.txCancel = nil
		}
		e.readOnly = false
		if err != nil {
			e.close()
			return nil, err
		}
		if policyErr := e.conn.(sqlite.AuthorizerRegisterer).RegisterAuthorizer(sqlPolicy(e)); err == nil {
			err = policyErr
		}
		return nil, err
	case "rows_close":
		if cur := e.cursors[in.Cursor]; cur != nil {
			g.closeCursor(e, in.Cursor, nil)
		}
		return nil, nil
	case "next":
		cur := e.cursors[in.Cursor]
		if cur == nil {
			return nil, fmt.Errorf("dbgate: cursor expired or closed")
		}
		stop := context.AfterFunc(ctx, cur.cancel)
		defer stop()
		return g.batch(e, in.Cursor)
	case "exec", "query":
		args, err := decodeArgs(in.Args)
		if err != nil {
			return nil, err
		}
		text := in.SQL
		var p driver.Stmt
		if in.Stmt != "" {
			prepared, ok := e.stmts[in.Stmt]
			if !ok {
				return nil, fmt.Errorf("dbgate: statement is closed")
			}
			p, text = prepared.stmt, prepared.text
		}
		finish := g.startQuery(botID, text)
		qctx, cancel := context.WithTimeout(s.ctx, g.timeout())
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		if method == "exec" {
			defer cancel()
			var r driver.Result
			if p == nil {
				r, err = e.conn.(driver.ExecerContext).ExecContext(qctx, text, args)
			} else {
				r, err = p.(driver.StmtExecContext).ExecContext(qctx, args)
			}
			finish(err)
			if err != nil {
				return nil, err
			}
			last, err := r.LastInsertId()
			if err != nil {
				return nil, err
			}
			affected, err := r.RowsAffected()
			return protocol.Result{LastID: last, Affected: affected}, err
		}
		if len(e.cursors) >= 16 {
			cancel()
			err := fmt.Errorf("dbgate: cursor limit reached")
			finish(err)
			return nil, err
		}
		var rows driver.Rows
		if p == nil {
			rows, err = e.conn.(driver.QueryerContext).QueryContext(qctx, text, args)
		} else {
			rows, err = p.(driver.StmtQueryContext).QueryContext(qctx, args)
		}
		if err != nil {
			cancel()
			finish(err)
			return nil, err
		}
		id := resourceID()
		e.cursors[id] = &sqlCursor{rows: rows, ctx: qctx, cancel: cancel, finish: finish}
		context.AfterFunc(qctx, func() {
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.cursors[id] != nil {
				withIdentity(e.uid, func() error { g.closeCursor(e, id, qctx.Err()); return nil })
			}
		})
		return g.batch(e, id)
	}
	return nil, wire.Errorf(wire.CodeNoMethod, "unknown database operation")
}

func (g *Gateway) closeCursor(e *sqlConn, id string, err error) {
	cur := e.cursors[id]
	delete(e.cursors, id)
	closeErr := cur.rows.Close()
	cur.cancel()
	if err == nil {
		err = closeErr
	}
	cur.finish(err)
}

func (g *Gateway) batch(e *sqlConn, id string) (protocol.Batch, error) {
	cur := e.cursors[id]
	b := protocol.Batch{Cursor: id, Columns: cur.rows.Columns(), Rows: [][]protocol.Value{}}
	if types, ok := cur.rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		for i := range b.Columns {
			b.Types = append(b.Types, types.ColumnTypeDatabaseTypeName(i))
		}
	}
	size := 0
	for len(b.Rows) < protocol.BatchSize {
		values := make([]driver.Value, len(b.Columns))
		err := cur.rows.Next(values)
		if errors.Is(err, io.EOF) {
			b.Done = true
			g.closeCursor(e, id, nil)
			return b, nil
		}
		if err != nil {
			g.closeCursor(e, id, err)
			return protocol.Batch{}, err
		}
		row := make([]protocol.Value, len(values))
		rowSize := 0
		for i, v := range values {
			encoded, err := protocol.EncodeResult(v)
			if err != nil {
				g.closeCursor(e, id, err)
				return protocol.Batch{}, err
			}
			row[i] = encoded
			rowSize += len(encoded.Text) + len(encoded.Blob) + 32
		}
		if rowSize > 8<<20 {
			err := fmt.Errorf("dbgate: result row exceeds 8 MiB")
			g.closeCursor(e, id, err)
			return protocol.Batch{}, err
		}
		b.Rows = append(b.Rows, row)
		size += rowSize
		if size >= 1<<20 {
			break
		}
	}
	return b, nil
}
