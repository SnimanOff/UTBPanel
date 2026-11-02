package bots

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/metrics"
	"github.com/SnimanOff/UTBPanel/backend/internal/panics"
)

type AlertFinding struct {
	RuleID int64     `json:"rule_id"`
	Time   time.Time `json:"time"`
	Value  float64   `json:"value"`
	Text   string    `json:"text"`
}
type Overview struct {
	core.Overview
	GroupID       int64           `json:"group_id,omitempty"`
	Panics        []panics.Group  `json:"panics"`
	LastReport    *metrics.Report `json:"last_report"`
	ReportPending bool            `json:"report_pending"`
	Rules         []alerts.Rule   `json:"rules"`
	Alerts        []AlertFinding  `json:"alerts"`
}
type OverviewAPI struct {
	Bots    *Service
	Metrics *metrics.Storage
	Panics  *panics.Store
	Rules   *alerts.Rules
}

func (a *OverviewAPI) Get(ctx context.Context, id int64) (Overview, error) {
	var out Overview
	if err := a.Bots.Call(ctx, "bots.overview", id, &out.Overview); err != nil {
		return out, err
	}
	metas, err := a.Bots.Groups.Metas(ctx)
	if err != nil {
		return out, err
	}
	out.GroupID = metas[id].GroupID
	out.Panics, err = a.Panics.Groups(ctx, id)
	if err != nil {
		return out, err
	}
	out.Panics = out.Panics[:min(len(out.Panics), 10)]
	for index := range out.Panics {
		p := &out.Panics[index]
		if len(p.LastStack) > 32<<10 {
			p.LastStack = strings.ToValidUTF8(p.LastStack[:32<<10], "") + "…"
		}
	}
	reports, err := a.Metrics.Reports(ctx, id)
	if err != nil {
		return out, err
	}
	if len(reports) > 0 {
		out.LastReport = &reports[0]
	}
	if d := out.LastDeploy; d != nil && (d.Result == "ok" || d.Result == "observing") {
		out.ReportPending = out.LastReport == nil || out.LastReport.DeployID != d.ID
	}
	out.Rules, err = a.Rules.List(ctx, id)
	if err != nil {
		return out, err
	}
	common, err := a.Rules.List(ctx, 0)
	if err != nil {
		return out, err
	}
	out.Rules = append(common, out.Rules...)
	rows, err := a.Metrics.DB.R.QueryContext(ctx, `SELECT l.rule_id,l.ts,l.value,l.text FROM alerts_log l JOIN alert_rules r ON r.id=l.rule_id WHERE r.enabled=1 AND l.bot_id=? AND l.state='fired' AND l.id=(SELECT MAX(n.id) FROM alerts_log n WHERE n.rule_id=l.rule_id AND n.bot_id=l.bot_id) ORDER BY l.id DESC LIMIT 20`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	out.Alerts = []AlertFinding{}
	for rows.Next() {
		var f AlertFinding
		var ts int64
		if err := rows.Scan(&f.RuleID, &ts, &f.Value, &f.Text); err != nil {
			return out, err
		}
		f.Time = time.Unix(ts, 0).UTC()
		out.Alerts = append(out.Alerts, f)
	}
	return out, rows.Err()
}
func (a *OverviewAPI) Register(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/overview", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		out, err := a.Get(ctx, id)
		if err != nil {
			WriteCoreError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		api.WriteJSON(w, 200, out)
	})
}
