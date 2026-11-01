package dbgate

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Requests over the core socket.
type (
	RowsReq struct {
		BotID  int64  `cbor:"1,keyasint"`
		Table  string `cbor:"2,keyasint"`
		Limit  int    `cbor:"3,keyasint"`
		Offset int    `cbor:"4,keyasint"`
	}
	QueryReq struct {
		BotID int64         `cbor:"1,keyasint"`
		SQL   string        `cbor:"2,keyasint"`
		Write bool          `cbor:"3,keyasint"`
		Actor journal.Actor `cbor:"9,keyasint"`
	}
	CSVReq struct {
		BotID int64  `cbor:"1,keyasint"`
		Table string `cbor:"2,keyasint,omitempty"`
		SQL   string `cbor:"3,keyasint,omitempty"`
	}
)

func wireErr(err error) error {
	if err == ErrNoDB {
		return wire.Errorf(wire.CodeNotFound, "у бота ещё нет базы данных")
	}
	if err != nil {
		if _, ok := err.(*wire.Error); !ok {
			return wire.Errorf("sql", "%v", err)
		}
	}
	return err
}

type chunkW struct{ s *wire.Sender }

func (c chunkW) Write(p []byte) (int, error) {
	if err := c.s.Send(append([]byte(nil), p...)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Register adds the core socket methods. Writes go to the core journal
// with the query text.
func (b *Browser) Register(mux *wire.Mux, j *journal.Journal) {
	mux.Handle("db.tables", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) (Info, error) {
		i, err := b.Tables(ctx, id)
		return i, wireErr(err)
	}))
	mux.Handle("db.rows", wire.Func(func(ctx context.Context, _ *wire.Conn, q RowsReq) (Result, error) {
		r, err := b.Rows(ctx, q.BotID, q.Table, q.Limit, q.Offset)
		return r, wireErr(err)
	}))
	mux.Handle("db.query", wire.Func(func(ctx context.Context, _ *wire.Conn, q QueryReq) (r Result, err error) {
		if !q.Write {
			r, err = b.Query(ctx, q.BotID, q.SQL, false)
			return r, wireErr(err)
		}
		err = j.Do(ctx, q.Actor, "db.write", q.BotID, map[string]string{"sql": q.SQL}, func() error {
			r, err = b.Query(ctx, q.BotID, q.SQL, true)
			return wireErr(err)
		})
		return r, err
	}))
	mux.HandleStream("db.csv", func(ctx context.Context, _ *wire.Conn, body wire.Raw, s *wire.Sender) error {
		var q CSVReq
		if err := wire.Unmarshal(body, &q); err != nil {
			return err
		}
		return wireErr(b.CSV(ctx, q.BotID, q.Table, q.SQL, chunkW{s}))
	})
}

// API is the panel side of the "Database" tab.
type API struct {
	Core     *wire.Client
	WriteErr func(http.ResponseWriter, error)
}

// RegisterAPI adds the routes.
func (a *API) RegisterAPI(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/db", a.tables)
	mux.HandleFunc("GET /api/bots/{id}/db/metrics", func(w http.ResponseWriter, r *http.Request) {
		var stats QueryStats
		if err := a.Core.Call(r.Context(), "db.metrics", id(r), &stats); err != nil {
			a.WriteErr(w, err)
			return
		}
		api.WriteJSON(w, http.StatusOK, stats)
	})
	mux.HandleFunc("GET /api/bots/{id}/db/tables/{table}", a.rows)
	mux.HandleFunc("POST /api/bots/{id}/db/query", a.query(false))
	mux.HandleFunc("POST /api/bots/{id}/db/exec", a.query(true))
	mux.HandleFunc("GET /api/bots/{id}/db/export", a.export)
}

func id(r *http.Request) int64 {
	n, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return n
}

func (a *API) tables(w http.ResponseWriter, r *http.Request) {
	var i Info
	if err := a.Core.Call(r.Context(), "db.tables", id(r), &i); err != nil {
		a.WriteErr(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, i)
}

func (a *API) rows(w http.ResponseWriter, r *http.Request) {
	q := RowsReq{BotID: id(r), Table: r.PathValue("table")}
	q.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	q.Offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	var res Result
	if err := a.Core.Call(r.Context(), "db.rows", q, &res); err != nil {
		a.WriteErr(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, res)
}

// query runs console SQL; exec (write) needs holding the button in the UI
// and is never available over MCP.
func (a *API) query(write bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			SQL string `json:"sql"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if write {
			audit.Annotate(r, "db.write", "", map[string]any{"sql": in.SQL})
		} else {
			audit.Skip(r) // reading is not an action
		}
		var res Result
		if err := a.Core.Call(r.Context(), "db.query", QueryReq{BotID: id(r), SQL: in.SQL, Write: write, Actor: api.Actor(r.Context())}, &res); err != nil {
			a.WriteErr(w, err)
			return
		}
		api.WriteJSON(w, http.StatusOK, res)
	}
}

func (a *API) export(w http.ResponseWriter, r *http.Request) {
	c := a.Core.Conn()
	if c == nil {
		a.WriteErr(w, wire.ErrNotConnected)
		return
	}
	q := CSVReq{BotID: id(r), Table: r.URL.Query().Get("table"), SQL: r.URL.Query().Get("sql")}
	st, err := c.OpenStream(r.Context(), "db.csv", q, wire.StreamOptions{Buffer: 256})
	if err != nil {
		a.WriteErr(w, err)
		return
	}
	defer st.Close()
	name := q.Table
	if name == "" {
		name = "query"
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
	for {
		var chunk []byte
		if err := st.Recv(&chunk); err != nil {
			return
		}
		if _, err := w.Write(chunk); err != nil {
			return
		}
	}
}
