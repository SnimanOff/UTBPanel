package metrics

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// Service is one external address of a bot over a period.
type Service struct {
	Host      string  `json:"host"`
	Conns     int64   `json:"connections"`
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
	P95       float64 `json:"connect_p95_ms"`
	In        int64   `json:"bytes_in"`
	Out       int64   `json:"bytes_out"`
	FirstSeen string  `json:"first_seen,omitempty"`
	Known     bool    `json:"known"`
	Bots      []int64 `json:"bots,omitempty"`
}

// services folds svc_<host>_* counters of minute rows.
func (s *Storage) services(ctx context.Context, botID int64, from time.Time, perBot bool) (map[string]*Service, error) {
	q := `SELECT bot_id, data FROM m_minute WHERE ts >= ? AND bot_id > 0`
	args := []any{from.Unix()}
	if botID > 0 {
		q += ` AND bot_id = ?`
		args = append(args, botID)
	}
	rows, err := s.DB.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Service{}
	hists := map[string]Hist{}
	bots := map[string]map[int64]bool{}
	for rows.Next() {
		var id int64
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		var m Minute
		if Decode(data, &m) != nil {
			continue
		}
		for k, v := range m.Counters {
			rest, ok := strings.CutPrefix(k, "svc_")
			if !ok {
				continue
			}
			i := strings.LastIndex(rest, "_")
			if i < 0 {
				continue
			}
			host, field := rest[:i], rest[i+1:]
			if strings.Contains(host, "_err") { // svc_<host>_err_<kind>
				continue
			}
			sv := out[host]
			if sv == nil {
				sv = &Service{Host: host}
				out[host] = sv
				bots[host] = map[int64]bool{}
			}
			bots[host][id] = true
			switch field {
			case "conn":
				sv.Conns += v
			case "err":
				sv.Errors += v
			case "in":
				sv.In += v
			case "out":
				sv.Out += v
			}
		}
		for k, h := range m.Hists {
			if host, ok := strings.CutPrefix(k, "svc_"); ok {
				hists[host] = hists[host].Merge(h)
			}
		}
	}
	for host, sv := range out {
		if sv.Conns > 0 {
			sv.ErrorRate = float64(sv.Errors) / float64(sv.Conns)
		}
		sv.P95 = hists[host].Percentile(0.95)
		if perBot {
			for b := range bots[host] {
				sv.Bots = append(sv.Bots, b)
			}
			sort.Slice(sv.Bots, func(i, j int) bool { return sv.Bots[i] < sv.Bots[j] })
		}
	}
	return out, rows.Err()
}

// RememberHost stores an address a bot connected to; it reports whether
// the address is new for this bot.
func (s *Storage) RememberHost(ctx context.Context, botID int64, host string) bool {
	res, err := s.DB.W.ExecContext(ctx, `INSERT OR IGNORE INTO known_hosts (bot_id, host, first_seen) VALUES (?, ?, ?)`, botID, host, store.Now())
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// OnCoreEvent handles service_seen from the core.
func (s *Storage) OnCoreEvent(ev events.Event) {
	if ev.Type != "service_seen" {
		return
	}
	host, _ := ev.Data["host"].(string)
	if host != "" && s.RememberHost(context.Background(), ev.BotID, host) {
		s.Log.Info("bot connected to a new address", "bot", ev.BotID, "host", host)
	}
}

// ServicesAPI serves the "Network" tab and the server summary.
type ServicesAPI struct{ S *Storage }

// Register adds the routes.
func (a *ServicesAPI) Register(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/services", a.bot)
	mux.HandleFunc("POST /api/bots/{id}/services/known", a.known)
	mux.HandleFunc("GET /api/server/services", a.server)
}

func (a *ServicesAPI) bot(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	p, ok := Periods[r.URL.Query().Get("period")]
	if !ok {
		p = Periods["24h"]
	}
	if p.Span == 0 || p.Span > 30*24*time.Hour {
		p.Span = 30 * 24 * time.Hour // minutes are kept 30 days
	}
	m, err := a.S.services(r.Context(), id, time.Now().Add(-p.Span), false)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	rows, err := a.S.DB.R.QueryContext(r.Context(), `SELECT host, first_seen, known FROM known_hosts WHERE bot_id = ?`, id)
	if err == nil {
		for rows.Next() {
			var host, first string
			var known bool
			rows.Scan(&host, &first, &known)
			sv := m[host]
			if sv == nil {
				sv = &Service{Host: host}
				m[host] = sv
			}
			sv.FirstSeen, sv.Known = first, known
		}
		rows.Close()
	}
	out := make([]Service, 0, len(m))
	for _, sv := range m {
		out = append(out, *sv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Conns > out[j].Conns })
	api.WriteJSON(w, http.StatusOK, out)
}

func (a *ServicesAPI) known(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in struct {
		Host string `json:"host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Host == "" {
		api.WriteError(w, http.StatusBadRequest, "bad_request", "host is required")
		return
	}
	_, err := a.S.DB.W.ExecContext(r.Context(), `INSERT INTO known_hosts (bot_id, host, first_seen, known) VALUES (?, ?, ?, 1)
		ON CONFLICT (bot_id, host) DO UPDATE SET known = 1`, id, in.Host, store.Now())
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	audit.Annotate(r, "service.known", in.Host, nil)
	w.WriteHeader(http.StatusNoContent)
}

// server: addresses across all bots over the last 10 minutes, e.g.
// "api.yookassa.ru: 3 bots, 12 % connection errors".
func (a *ServicesAPI) server(w http.ResponseWriter, r *http.Request) {
	m, err := a.S.services(r.Context(), 0, time.Now().Add(-10*time.Minute), true)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]Service, 0, len(m))
	for _, sv := range m {
		out = append(out, *sv)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ErrorRate > out[j].ErrorRate || (out[i].ErrorRate == out[j].ErrorRate && out[i].Conns > out[j].Conns)
	})
	api.WriteJSON(w, http.StatusOK, out)
}
