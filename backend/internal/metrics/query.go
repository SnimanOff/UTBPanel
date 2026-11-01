package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/axiomhq/hyperloglog"
)

// Period of charts: Live / 1 h / 24 h / 7 d / 30 d / 90 d / 1 y / all.
type Period struct {
	Name  string
	Span  time.Duration // 0 = all
	Table string        // m_minute | m_hour | m_day
	Step  int64         // seconds per point
}

// Periods by name.
var Periods = map[string]Period{
	"1h":  {"1h", time.Hour, "m_minute", 60},
	"24h": {"24h", 24 * time.Hour, "m_minute", 60},
	"7d":  {"7d", 7 * 24 * time.Hour, "m_hour", 3600},
	"30d": {"30d", 30 * 24 * time.Hour, "m_hour", 3600},
	"90d": {"90d", 90 * 24 * time.Hour, "m_day", 86400},
	"1y":  {"1y", 365 * 24 * time.Hour, "m_day", 86400},
	"all": {"all", 0, "m_day", 86400},
}

// Point is one point of a chart.
type Point struct {
	TS       int64              `json:"ts"`
	Counters map[string]int64   `json:"c,omitempty"`
	Gauges   map[string]float64 `json:"g,omitempty"`
	P50      float64            `json:"p50,omitempty"`
	P95      float64            `json:"p95,omitempty"`
	P99      float64            `json:"p99,omitempty"`
}

// Deploy is a mark on every chart.
type Deploy struct {
	TS      int64  `json:"ts"`
	Version string `json:"version"`
	Result  string `json:"result,omitempty"`
}

// Series is the answer for a bot (or bot 0, all bots) and a period.
type Series struct {
	Period  string           `json:"period"`
	Step    int64            `json:"step"`
	Points  []Point          `json:"points"`
	Totals  map[string]int64 `json:"totals"`
	P50     float64          `json:"p50"`
	P95     float64          `json:"p95"`
	P99     float64          `json:"p99"`
	Uniques map[string]int64 `json:"uniques"` // dau, wau, mau, period
	Deploys []Deploy         `json:"deploys"`
}

// Query returns the series of a bot for a period.
func (s *Storage) Query(ctx context.Context, botID int64, periodName string, now time.Time) (Series, error) {
	p, ok := Periods[periodName]
	if !ok {
		return Series{}, fmt.Errorf("metrics: unknown period %q", periodName)
	}
	var from int64
	if p.Span > 0 {
		from = now.Add(-p.Span).Unix()
	}
	col, fromKey := "ts", from
	if p.Table == "m_day" {
		col, fromKey = "day", from/86400
	}
	rows, err := s.DB.R.QueryContext(ctx, `SELECT `+col+`, data FROM `+p.Table+` WHERE bot_id = ? AND `+col+` >= ? ORDER BY `+col, botID, fromKey)
	if err != nil {
		return Series{}, err
	}
	defer rows.Close()
	out := Series{Period: p.Name, Step: p.Step, Points: []Point{}, Totals: map[string]int64{}, Uniques: map[string]int64{}, Deploys: []Deploy{}}
	total := NewHist()
	for rows.Next() {
		var key int64
		var data []byte
		if err := rows.Scan(&key, &data); err != nil {
			return out, err
		}
		var m Minute
		if Decode(data, &m) != nil {
			continue
		}
		ts := key
		if col == "day" {
			ts = key * 86400
		}
		pt := Point{TS: ts, Counters: m.Counters, Gauges: m.Gauges}
		if h := m.Hists[RespTime]; h.Count() > 0 {
			pt.P50, pt.P95, pt.P99 = h.Percentile(0.5), h.Percentile(0.95), h.Percentile(0.99)
			total = total.Merge(h)
		}
		for k, v := range m.Counters {
			out.Totals[k] += v
		}
		out.Points = append(out.Points, pt)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.P50, out.P95, out.P99 = total.Percentile(0.5), total.Percentile(0.95), total.Percentile(0.99)
	today := now.Unix() / 86400
	for name, days := range map[string]int64{"dau": 1, "wau": 7, "mau": 30} {
		out.Uniques[name] = s.uniques(ctx, botID, today-days+1, today, UniqUsers)
	}
	periodFrom := from / 86400
	if p.Span == 0 {
		periodFrom = 0
	}
	out.Uniques["period"] = s.uniques(ctx, botID, periodFrom, today, UniqUsers)
	drows, err := s.DB.R.QueryContext(ctx, `SELECT ts, version, result FROM deploys WHERE bot_id = ? AND ts >= ? ORDER BY ts`, botID, from)
	if err == nil {
		defer drows.Close()
		for drows.Next() {
			var d Deploy
			drows.Scan(&d.TS, &d.Version, &d.Result)
			out.Deploys = append(out.Deploys, d)
		}
	}
	return out, nil
}

// uniques merges daily HyperLogLog sketches: unique users over any period.
func (s *Storage) uniques(ctx context.Context, botID, fromDay, toDay int64, kind string) int64 {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT hll FROM m_uniq WHERE bot_id = ? AND kind = ? AND day >= ? AND day <= ?`, botID, kind, fromDay, toDay)
	if err != nil {
		return 0
	}
	defer rows.Close()
	sum := hyperloglog.New14()
	for rows.Next() {
		var b []byte
		rows.Scan(&b)
		var sk hyperloglog.Sketch
		if sk.UnmarshalBinary(b) == nil {
			sum.Merge(&sk)
		}
	}
	return int64(sum.Estimate())
}

// AddDeploy stores a deploy mark.
func (s *Storage) AddDeploy(ctx context.Context, botID int64, at time.Time, version, result string) error {
	_, err := s.DB.W.ExecContext(ctx, `INSERT INTO deploys (bot_id, ts, version, result) VALUES (?, ?, ?, ?)`, botID, at.Unix(), version, result)
	return err
}
