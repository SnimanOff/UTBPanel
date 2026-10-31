// Package audit is the panel's action log: every changing request with the
// key name, IP and source, plus the core's journal (CLI over SSH,
// autorollback, schedule) merged in.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Entry is one audit row.
type Entry struct {
	ID      int64           `json:"id"`
	Time    time.Time       `json:"ts"`
	Key     string          `json:"key"`
	IP      string          `json:"ip,omitempty"`
	Source  string          `json:"source"`
	Action  string          `json:"action"`
	BotID   int64           `json:"bot_id,omitempty"`
	Object  string          `json:"object,omitempty"`
	Details json.RawMessage `json:"details,omitempty"`
	Result  string          `json:"result"`
}

// Log writes the audit.
type Log struct {
	DB  *store.DB
	W   *store.Writer
	Log *slog.Logger
}

// Write stores an entry at once (audit is an important record).
func (l *Log) Write(ctx context.Context, e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	det := string(e.Details)
	if det == "" {
		det = "{}"
	}
	var bot any
	if e.BotID > 0 {
		bot = e.BotID
	}
	return l.W.Now(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO audit (ts, key, ip, source, action, bot_id, object, details, result) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			store.FormatTime(e.Time), e.Key, e.IP, e.Source, e.Action, bot, e.Object, det, e.Result)
		return err
	})
}

// note lets handlers describe what the request did.
type note struct {
	mu      sync.Mutex
	action  string
	object  string
	botID   int64
	details map[string]any
	skip    bool
}

type noteKey struct{}

// Annotate sets the action, object and details of the current request.
// Secret values must never be passed: use "изменено" instead.
func Annotate(r *http.Request, action, object string, details map[string]any) {
	n, _ := r.Context().Value(noteKey{}).(*note)
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if action != "" {
		n.action = action
	}
	if object != "" {
		n.object = object
	}
	if n.details == nil {
		n.details = map[string]any{}
	}
	for k, v := range details {
		n.details[k] = v
	}
}

// SetBot ties the request to a bot.
func SetBot(r *http.Request, id int64) {
	if n, _ := r.Context().Value(noteKey{}).(*note); n != nil {
		n.mu.Lock()
		n.botID = id
		n.mu.Unlock()
	}
}

// Skip leaves the request out of the audit (for example, a pure query sent
// with POST).
func Skip(r *http.Request) {
	if n, _ := r.Context().Value(noteKey{}).(*note); n != nil {
		n.skip = true
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Middleware records every changing request together with its result.
func (l *Log) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		n := &note{}
		sw := &statusWriter{ResponseWriter: w}
		r = r.WithContext(context.WithValue(r.Context(), noteKey{}, n))
		next.ServeHTTP(sw, r)
		if n.skip {
			return
		}
		c := api.CallerFrom(r.Context())
		action := n.action
		if action == "" {
			action = r.Method + " " + r.URL.Path
		}
		if n.botID == 0 {
			n.botID = botFromPath(r.URL.Path)
		}
		result := "ok"
		if sw.status >= 400 {
			result = "error " + strconv.Itoa(sw.status)
		}
		e := Entry{Key: c.Name, IP: c.IP, Source: string(c.Source), Action: action, BotID: n.botID, Object: n.object, Result: result}
		if c.Name == "" {
			e.Key = "-"
		}
		if n.details != nil {
			e.Details, _ = json.Marshal(n.details)
		}
		if err := l.Write(context.WithoutCancel(r.Context()), e); err != nil {
			l.Log.Error("audit write failed", "err", err, "action", action)
		}
	})
}

// botFromPath reads {id} from /api/bots/{id}/….
func botFromPath(p string) int64 {
	rest, ok := strings.CutPrefix(p, "/api/bots/")
	if !ok {
		return 0
	}
	id, _ := strconv.ParseInt(strings.SplitN(rest, "/", 2)[0], 10, 64)
	return id
}

// Query selects audit rows.
type Query struct {
	BotID  int64
	Action string
	Source string
	From   time.Time
	To     time.Time
	Limit  int
	Before int64 // id cursor for pages
}

// List returns entries, newest first.
func (l *Log) List(ctx context.Context, q Query) ([]Entry, error) {
	where := []string{"1=1"}
	var args []any
	if q.BotID > 0 {
		where, args = append(where, "bot_id = ?"), append(args, q.BotID)
	}
	if q.Action != "" {
		where, args = append(where, "action LIKE ?"), append(args, "%"+q.Action+"%")
	}
	if q.Source != "" {
		where, args = append(where, "source = ?"), append(args, q.Source)
	}
	if !q.From.IsZero() {
		where, args = append(where, "ts >= ?"), append(args, store.FormatTime(q.From))
	}
	if !q.To.IsZero() {
		where, args = append(where, "ts <= ?"), append(args, store.FormatTime(q.To))
	}
	if q.Before > 0 {
		where, args = append(where, "id < ?"), append(args, q.Before)
	}
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	args = append(args, q.Limit)
	rows, err := l.DB.R.QueryContext(ctx, `SELECT id, ts, key, ip, source, action, COALESCE(bot_id, 0), object, details, result FROM audit WHERE `+
		strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var ts, det string
		if err := rows.Scan(&e.ID, &ts, &e.Key, &e.IP, &e.Source, &e.Action, &e.BotID, &e.Object, &det, &e.Result); err != nil {
			return nil, err
		}
		e.Time, _ = store.ParseTime(ts)
		if det != "{}" {
			e.Details = json.RawMessage(det)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Register adds GET /api/audit.
func (l *Log) Register(mux *api.Mux) {
	mux.HandleFunc("GET /api/audit", func(w http.ResponseWriter, r *http.Request) {
		v := r.URL.Query()
		q := Query{Action: v.Get("action"), Source: v.Get("source")}
		q.BotID, _ = strconv.ParseInt(v.Get("bot"), 10, 64)
		q.Limit, _ = strconv.Atoi(v.Get("limit"))
		q.Before, _ = strconv.ParseInt(v.Get("before"), 10, 64)
		q.From, _ = time.Parse(time.RFC3339, v.Get("from"))
		q.To, _ = time.Parse(time.RFC3339, v.Get("to"))
		es, err := l.List(r.Context(), q)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		api.WriteJSON(w, http.StatusOK, es)
	})
}

const lastJournalKey = "journal_last_id"

// MergeJournal copies new core journal entries into the audit. Entries
// made on behalf of the panel are skipped: the panel audited them itself.
func (l *Log) MergeJournal(ctx context.Context, c *wire.Conn) error {
	for {
		var last int64
		var s string
		if err := l.DB.R.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, lastJournalKey).Scan(&s); err == nil {
			last, _ = strconv.ParseInt(s, 10, 64)
		}
		var es []journal.Entry
		if err := c.Call(ctx, journal.MethodSince, last, &es); err != nil {
			return err
		}
		if len(es) == 0 {
			return nil
		}
		err := l.W.Now(ctx, func(tx *sql.Tx) error {
			for _, e := range es {
				if e.Actor.Source == journal.SourceUI || e.Actor.Source == journal.SourceMCP {
					continue
				}
				var bot any
				if e.BotID > 0 {
					bot = e.BotID
				}
				args := string(e.Args)
				if args == "" {
					args = "{}"
				}
				key := e.Actor.Name
				if key == "" {
					key = e.Actor.Source
				}
				if _, err := tx.Exec(`INSERT INTO audit (ts, key, ip, source, action, bot_id, object, details, result) VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)`,
					store.FormatTime(e.Time), key, e.Actor.IP, e.Actor.Source, e.Command, bot, args, e.Result); err != nil {
					return err
				}
			}
			_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
				lastJournalKey, strconv.FormatInt(es[len(es)-1].ID, 10))
			return err
		})
		if err != nil || len(es) < 1000 {
			return err
		}
	}
}
