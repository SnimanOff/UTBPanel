package metrics

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/procstat"
)

// ServerPoint is one point of server charts (maximum within the step).
type ServerPoint struct {
	TS     int64              `json:"ts"`
	Gauges map[string]float64 `json:"g"`
}

// QueryServer returns host metrics for 1h / 24h / 7d / 30d, at most about
// 500 points (minutes folded by maximum).
func (s *Storage) QueryServer(ctx context.Context, period string, now time.Time) ([]ServerPoint, int64, error) {
	p, ok := Periods[period]
	if !ok || p.Span == 0 || p.Span > 30*24*time.Hour {
		return nil, 0, fmt.Errorf("metrics: server history covers 1h, 24h, 7d, 30d")
	}
	from := now.Add(-p.Span).Unix()
	step := max(int64(60), int64(p.Span.Seconds())/500/60*60)
	rows, err := s.DB.R.QueryContext(ctx, `SELECT ts, data FROM m_server WHERE ts >= ? ORDER BY ts`, from)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ServerPoint{}
	for rows.Next() {
		var ts int64
		var data []byte
		if err := rows.Scan(&ts, &data); err != nil {
			return nil, 0, err
		}
		var m Minute
		if Decode(data, &m) != nil {
			continue
		}
		b := ts / step * step
		if n := len(out); n > 0 && out[n-1].TS == b {
			for k, v := range m.Gauges {
				out[n-1].Gauges[k] = max(out[n-1].Gauges[k], v)
			}
			continue
		}
		g := map[string]float64{}
		for k, v := range m.Gauges {
			g[k] = v
		}
		out = append(out, ServerPoint{TS: b, Gauges: g})
	}
	return out, step, rows.Err()
}

// BotLoad is a row of the server page table.
type BotLoad struct {
	BotID   int64            `json:"bot_id"`
	Updates int64            `json:"updates_24h"`
	Errors  int64            `json:"errors_24h"`
	CPU     float64          `json:"cpu_max"`
	RSS     float64          `json:"rss_max"`
	Revenue map[string]int64 `json:"revenue_24h"`
}

// Loads sums the last 24 hours per bot.
func (s *Storage) Loads(ctx context.Context, now time.Time) ([]BotLoad, error) {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT bot_id, data FROM m_minute WHERE bot_id > 0 AND ts >= ?`, now.Add(-24*time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[int64]*BotLoad{}
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
		l := by[id]
		if l == nil {
			l = &BotLoad{BotID: id, Revenue: map[string]int64{}}
			by[id] = l
		}
		l.Updates += m.Counters[Updates]
		l.Errors += m.Counters[Errors] + m.Counters[Timeouts]
		l.CPU = max(l.CPU, m.Gauges[CPU])
		l.RSS = max(l.RSS, m.Gauges[RSS])
		for k, v := range m.Counters {
			if len(k) > 8 && k[:4] == PaymentsPrefix && k[len(k)-4:] == "_sum" {
				l.Revenue[k[4:len(k)-4]] += v
			}
		}
	}
	out := []BotLoad{}
	for _, l := range by {
		out = append(out, *l)
	}
	return out, rows.Err()
}

// ServerAPI serves the "Server" page.
type ServerAPI struct {
	S     *Storage
	Procs func(ctx context.Context) ([]procstat.Stats, error)
}

// Register adds the routes.
func (a *ServerAPI) Register(mux *api.Mux) {
	mux.HandleFunc("GET /api/server/metrics", func(w http.ResponseWriter, r *http.Request) {
		period := r.URL.Query().Get("period")
		if period == "" {
			period = "24h"
		}
		pts, step, err := a.S.QueryServer(r.Context(), period, time.Now())
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		all, _ := a.S.Query(r.Context(), 0, period, time.Now())
		procs, _ := a.Procs(r.Context())
		if procs == nil {
			procs = []procstat.Stats{}
		}
		api.WriteJSON(w, http.StatusOK, map[string]any{"period": period, "step": step, "points": pts, "bots": all, "procs": procs})
	})
	mux.HandleFunc("GET /api/server/bots", func(w http.ResponseWriter, r *http.Request) {
		ls, err := a.S.Loads(r.Context(), time.Now())
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		api.WriteJSON(w, http.StatusOK, ls)
	})
}
