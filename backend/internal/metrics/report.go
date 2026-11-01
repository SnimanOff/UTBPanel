package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
)

// Crashes counts falls of the bot process (counter).
const Crashes = "crashes"

// ReportAfter is how long after a deploy the report is made; the same
// length before the deploy is the baseline.
var ReportAfter = time.Hour

// Summary is one side of a deploy report.
type Summary struct {
	Minutes int     `json:"minutes"` // minutes with data
	Updates int64   `json:"updates"`
	Errors  int64   `json:"errors"` // errors and timeouts
	ErrRate float64 `json:"err_rate"`
	P95     float64 `json:"p95_ms"`
	Crashes int64   `json:"crashes"`
	CPU     float64 `json:"cpu_avg"`
	RSS     float64 `json:"rss_max"`
}

// Report compares the hour before and after a deploy.
type Report struct {
	DeployID int64    `json:"deploy_id"`
	TS       int64    `json:"ts"`
	Version  string   `json:"version"`
	Before   Summary  `json:"before"`
	After    Summary  `json:"after"`
	Worse    []string `json:"worse"`
}

// Summarize sums minutes in [from, to).
func (s *Storage) Summarize(ctx context.Context, botID int64, from, to time.Time) (Summary, error) {
	var out Summary
	rows, err := s.DB.R.QueryContext(ctx, `SELECT data FROM m_minute WHERE bot_id = ? AND ts >= ? AND ts < ?`, botID, from.Unix(), to.Unix())
	if err != nil {
		return out, err
	}
	defer rows.Close()
	h := NewHist()
	var cpu float64
	for rows.Next() {
		var data []byte
		if rows.Scan(&data) != nil {
			continue
		}
		var m Minute
		if Decode(data, &m) != nil {
			continue
		}
		out.Minutes++
		out.Updates += m.Counters[Updates]
		out.Errors += m.Counters[Errors] + m.Counters[Timeouts]
		out.Crashes += m.Counters[Crashes]
		cpu += m.Gauges[CPU]
		out.RSS = max(out.RSS, m.Gauges[RSS])
		if r := m.Hists[RespTime]; r.Count() > 0 {
			h = h.Merge(r)
		}
	}
	if out.Minutes > 0 {
		out.CPU = cpu / float64(out.Minutes)
	}
	if out.Updates > 0 {
		out.ErrRate = float64(out.Errors) / float64(out.Updates)
	}
	out.P95 = h.Percentile(0.95)
	return out, rows.Err()
}

// Worse lists noticeable worsening: p95 or the error share doubled, or new
// crashes. Small numbers are not judged.
func Worse(before, after Summary) []string {
	out := []string{}
	if before.P95 > 0 && after.P95 >= 2*before.P95 && after.Updates >= 20 {
		out = append(out, fmt.Sprintf("p95 %.0f → %.0f мс", before.P95, after.P95))
	}
	if after.Errors >= 5 && (before.ErrRate == 0 && after.ErrRate > 0.01 || before.ErrRate > 0 && after.ErrRate >= 2*before.ErrRate) {
		out = append(out, fmt.Sprintf("ошибки %.1f%% → %.1f%%", before.ErrRate*100, after.ErrRate*100))
	}
	if after.Crashes > before.Crashes {
		out = append(out, fmt.Sprintf("падений %d → %d", before.Crashes, after.Crashes))
	}
	return out
}

// MakeReports writes reports for deploys that are an hour old. emit
// reports a worsening as an event.
func (s *Storage) MakeReports(ctx context.Context, now time.Time, emit func(botID int64, data map[string]any)) error {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT d.id, d.bot_id, d.ts, d.version FROM deploys d
		LEFT JOIN deploy_reports r ON r.deploy_id = d.id
		WHERE r.deploy_id IS NULL AND d.result IN ('ok', 'observing') AND d.ts <= ? AND d.ts > ?`,
		now.Add(-ReportAfter).Unix(), now.Add(-7*24*time.Hour).Unix())
	if err != nil {
		return err
	}
	type due struct {
		id, bot, ts int64
		version     string
	}
	var list []due
	for rows.Next() {
		var d due
		if rows.Scan(&d.id, &d.bot, &d.ts, &d.version) == nil {
			list = append(list, d)
		}
	}
	rows.Close()
	for _, d := range list {
		at := time.Unix(d.ts, 0)
		// A later deploy within the hour makes this one's "after" meaningless.
		var newer int
		s.DB.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM deploys WHERE bot_id = ? AND ts > ? AND ts < ? AND id != ?`,
			d.bot, d.ts, at.Add(ReportAfter).Unix(), d.id).Scan(&newer)
		before, err := s.Summarize(ctx, d.bot, at.Add(-ReportAfter), at)
		if err != nil {
			return err
		}
		after, err := s.Summarize(ctx, d.bot, at, at.Add(ReportAfter))
		if err != nil {
			return err
		}
		worse := Worse(before, after)
		if newer > 0 {
			worse = []string{}
		}
		bj, _ := json.Marshal(before)
		aj, _ := json.Marshal(after)
		wj, _ := json.Marshal(worse)
		if _, err := s.DB.W.ExecContext(ctx, `INSERT OR IGNORE INTO deploy_reports (deploy_id, bot_id, ts, version, before, after, worse) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			d.id, d.bot, d.ts, d.version, string(bj), string(aj), string(wj)); err != nil {
			return err
		}
		if len(worse) > 0 && emit != nil {
			emit(d.bot, map[string]any{"version": d.version, "worse": worse})
		}
	}
	return nil
}

// RunReports checks for due reports every 5 minutes.
func (s *Storage) RunReports(ctx context.Context, emit func(botID int64, data map[string]any)) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		if err := s.MakeReports(ctx, time.Now(), emit); err != nil && ctx.Err() == nil && s.Log != nil {
			s.Log.Warn("deploy reports failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Reports returns a bot's reports, newest first.
func (s *Storage) Reports(ctx context.Context, botID int64) ([]Report, error) {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT deploy_id, ts, version, before, after, worse FROM deploy_reports WHERE bot_id = ? ORDER BY ts DESC LIMIT 20`, botID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Report{}
	for rows.Next() {
		var r Report
		var b, a, w string
		if err := rows.Scan(&r.DeployID, &r.TS, &r.Version, &b, &a, &w); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(b), &r.Before)
		json.Unmarshal([]byte(a), &r.After)
		json.Unmarshal([]byte(w), &r.Worse)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReportsAPI serves GET /api/bots/{id}/deploy-reports.
type ReportsAPI struct{ S *Storage }

// Register adds the route.
func (a *ReportsAPI) Register(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/deploy-reports", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		rs, err := a.S.Reports(r.Context(), id)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		api.WriteJSON(w, http.StatusOK, rs)
	})
}

// Revenue sums payments (pay_<currency>_sum, all currencies) in [from, to).
func (s *Storage) Revenue(ctx context.Context, botID int64, from, to time.Time) (int64, error) {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT data FROM m_minute WHERE bot_id = ? AND ts >= ? AND ts < ?`, botID, from.Unix(), to.Unix())
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var sum int64
	for rows.Next() {
		var data []byte
		var m Minute
		if rows.Scan(&data) != nil || Decode(data, &m) != nil {
			continue
		}
		for k, v := range m.Counters {
			if strings.HasPrefix(k, PaymentsPrefix) && strings.HasSuffix(k, "_sum") {
				sum += v
			}
		}
	}
	return sum, rows.Err()
}
