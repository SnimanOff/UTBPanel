package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/metrics"
)

// Rule is a threshold: metric, condition and duration. BotID 0 applies to
// every bot.
type Rule struct {
	ID      int64   `json:"id"`
	BotID   int64   `json:"bot_id"`
	Metric  string  `json:"metric"`
	Op      string  `json:"op"`
	Value   float64 `json:"value"`
	Minutes int     `json:"minutes"`
	Enabled bool    `json:"enabled"`
}

// Metrics a rule can watch, with their words for texts.
var Metrics = map[string]string{
	"p95":          "p95 времени ответа, мс",
	"err_rate":     "доля ошибок, %",
	"cpu":          "CPU, %",
	"rss":          "RAM, МБ",
	"updates":      "апдейтов",
	"silence":      "тишина: ни одного апдейта",
	"revenue_drop": "падение дохода за сутки, %",
}

func (r Rule) valid() error {
	if _, ok := Metrics[r.Metric]; !ok {
		return fmt.Errorf("неизвестная метрика %q", r.Metric)
	}
	if r.Op != ">" && r.Op != "<" {
		return fmt.Errorf("условие > или <")
	}
	if r.Minutes < 1 || r.Minutes > 24*60 {
		return fmt.Errorf("длительность от 1 минуты до суток")
	}
	return nil
}

// Describe is the rule in words: "p95 времени ответа, мс > 2000 за 5 мин".
func (r Rule) Describe() string {
	if r.Metric == "silence" {
		return fmt.Sprintf("ни одного апдейта %d мин", r.Minutes)
	}
	if r.Metric == "revenue_drop" {
		return fmt.Sprintf("доход за сутки упал больше чем на %g%%", r.Value)
	}
	return fmt.Sprintf("%s %s %g за %d мин", Metrics[r.Metric], r.Op, r.Value, r.Minutes)
}

// Rules keeps rules and checks them once a minute.
type Rules struct {
	S     *metrics.Storage
	Notes *Service
	// Bots lists the bots to check rules with BotID 0.
	Bots func(ctx context.Context) []int64
	Now  func() time.Time
}

func (rs *Rules) now() time.Time {
	if rs.Now != nil {
		return rs.Now()
	}
	return time.Now()
}

// List returns the rules of a bot (and the common ones), or all with -1.
func (rs *Rules) List(ctx context.Context, botID int64) ([]Rule, error) {
	q, args := `SELECT id, bot_id, metric, op, value, minutes, enabled FROM alert_rules`, []any{}
	if botID >= 0 {
		q += ` WHERE bot_id = ?`
		args = append(args, botID)
	}
	rows, err := rs.S.DB.R.QueryContext(ctx, q+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.BotID, &r.Metric, &r.Op, &r.Value, &r.Minutes, &r.Enabled); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Save adds or replaces a rule.
func (rs *Rules) Save(ctx context.Context, r Rule) (Rule, error) {
	if r.Metric == "silence" {
		r.Op, r.Value = ">", 0
	}
	if r.Op == "" {
		r.Op = ">"
	}
	if err := r.valid(); err != nil {
		return r, err
	}
	if r.ID == 0 {
		res, err := rs.S.DB.W.ExecContext(ctx, `INSERT INTO alert_rules (bot_id, metric, op, value, minutes, enabled) VALUES (?, ?, ?, ?, ?, ?)`,
			r.BotID, r.Metric, r.Op, r.Value, r.Minutes, r.Enabled)
		if err != nil {
			return r, err
		}
		r.ID, _ = res.LastInsertId()
		return r, nil
	}
	_, err := rs.S.DB.W.ExecContext(ctx, `UPDATE alert_rules SET metric = ?, op = ?, value = ?, minutes = ?, enabled = ? WHERE id = ?`,
		r.Metric, r.Op, r.Value, r.Minutes, r.Enabled, r.ID)
	return r, err
}

// Delete removes a rule; an open alert of it is closed.
func (rs *Rules) Delete(ctx context.Context, id int64) error {
	_, err := rs.S.DB.W.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, id)
	rs.S.DB.W.ExecContext(ctx, `UPDATE notifications SET resolved = 1 WHERE key LIKE ?`, "alert:"+strconv.FormatInt(id, 10)+":%")
	return err
}

// Measure computes the rule's metric for a bot over its window.
func (rs *Rules) Measure(ctx context.Context, r Rule, botID int64) (value float64, enough bool, err error) {
	now := rs.now().Truncate(time.Minute)
	if r.Metric == "revenue_drop" {
		today, err := rs.S.Revenue(ctx, botID, now.Add(-24*time.Hour), now)
		if err != nil {
			return 0, false, err
		}
		before, err := rs.S.Revenue(ctx, botID, now.Add(-48*time.Hour), now.Add(-24*time.Hour))
		if err != nil || before <= 0 {
			return 0, false, err
		}
		return float64(before-today) * 100 / float64(before), true, nil
	}
	sum, err := rs.S.Summarize(ctx, botID, now.Add(-time.Duration(r.Minutes)*time.Minute), now)
	if err != nil {
		return 0, false, err
	}
	switch r.Metric {
	case "p95":
		return sum.P95, sum.Updates > 0, nil
	case "err_rate":
		return sum.ErrRate * 100, sum.Updates >= 10, nil
	case "cpu":
		return sum.CPU, sum.Minutes > 0, nil
	case "rss":
		return sum.RSS / (1 << 20), sum.Minutes > 0, nil
	case "updates":
		return float64(sum.Updates), true, nil
	case "silence":
		// Only for bots that had updates in the day before the window.
		day, err := rs.S.Summarize(ctx, botID, now.Add(-24*time.Hour), now.Add(-time.Duration(r.Minutes)*time.Minute))
		if err != nil || day.Updates == 0 {
			return 0, false, err
		}
		if sum.Updates == 0 {
			return 1, true, nil
		}
		return 0, true, nil
	}
	return 0, false, nil
}

func (r Rule) holds(v float64) bool {
	if r.Metric == "silence" {
		return v > 0
	}
	if r.Op == "<" {
		return v < r.Value
	}
	return v > r.Value
}

// Check evaluates every enabled rule; a change of state is logged and
// notified (fired, then "back to normal").
func (rs *Rules) Check(ctx context.Context) error {
	rules, err := rs.List(ctx, -1)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		bots := []int64{r.BotID}
		if r.BotID == 0 && rs.Bots != nil {
			bots = rs.Bots(ctx)
		}
		for _, bot := range bots {
			v, enough, err := rs.Measure(ctx, r, bot)
			if err != nil || !enough {
				continue
			}
			firing := rs.firing(ctx, r.ID, bot)
			key := "alert:" + strconv.FormatInt(r.ID, 10) + ":" + strconv.FormatInt(bot, 10)
			href := "/bots/" + strconv.FormatInt(bot, 10)
			switch {
			case r.holds(v) && !firing:
				text := "порог: " + r.Describe()
				if r.Metric != "silence" {
					text += fmt.Sprintf(" (сейчас %.1f)", v)
				}
				rs.log(ctx, r.ID, bot, "fired", v, text)
				rs.Notes.Notify(ctx, bot, Note{Kind: "alert", Severity: "error", Key: key, Text: text, Href: href})
			case !r.holds(v) && firing:
				rs.log(ctx, r.ID, bot, "ok", v, "в норме: "+r.Describe())
				rs.Notes.Notify(ctx, bot, Note{Resolves: []string{key}, Href: href})
			}
		}
	}
	return nil
}

func (rs *Rules) firing(ctx context.Context, ruleID, botID int64) bool {
	var state string
	rs.S.DB.R.QueryRowContext(ctx, `SELECT state FROM alerts_log WHERE rule_id = ? AND bot_id = ? ORDER BY id DESC LIMIT 1`, ruleID, botID).Scan(&state)
	return state == "fired"
}

func (rs *Rules) log(ctx context.Context, ruleID, botID int64, state string, v float64, text string) {
	rs.S.DB.W.ExecContext(ctx, `INSERT INTO alerts_log (rule_id, bot_id, ts, state, value, text) VALUES (?, ?, ?, ?, ?, ?)`,
		ruleID, botID, rs.now().Unix(), state, v, text)
}

// Run checks every minute.
func (rs *Rules) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rs.Check(ctx)
		}
	}
}

// LogEntry is one firing or recovery.
type LogEntry struct {
	RuleID int64     `json:"rule_id"`
	BotID  int64     `json:"bot_id"`
	TS     time.Time `json:"ts"`
	State  string    `json:"state"`
	Value  float64   `json:"value"`
	Text   string    `json:"text"`
}

// Register adds the routes.
func (rs *Rules) Register(mux *api.Mux) {
	mux.HandleFunc("GET /api/alert-rules", func(w http.ResponseWriter, r *http.Request) {
		bot := int64(-1)
		if v := r.URL.Query().Get("bot"); v != "" {
			bot, _ = strconv.ParseInt(v, 10, 64)
		}
		list, err := rs.List(r.Context(), bot)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		api.WriteJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("POST /api/alert-rules", func(w http.ResponseWriter, r *http.Request) {
		var in Rule
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		out, err := rs.Save(r.Context(), in)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		api.WriteJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("DELETE /api/alert-rules/{rid}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("rid"), 10, 64)
		if err := rs.Delete(r.Context(), id); err != nil {
			api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/alerts/log", func(w http.ResponseWriter, r *http.Request) {
		q, args := `SELECT rule_id, bot_id, ts, state, value, text FROM alerts_log`, []any{}
		if v := r.URL.Query().Get("bot"); v != "" {
			id, _ := strconv.ParseInt(v, 10, 64)
			q, args = q+` WHERE bot_id = ?`, append(args, id)
		}
		rows, err := rs.S.DB.R.QueryContext(r.Context(), q+` ORDER BY id DESC LIMIT 100`, args...)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		defer rows.Close()
		out := []LogEntry{}
		for rows.Next() {
			var e LogEntry
			var ts int64
			if rows.Scan(&e.RuleID, &e.BotID, &ts, &e.State, &e.Value, &e.Text) == nil {
				e.TS = time.Unix(ts, 0).UTC()
				out = append(out, e)
			}
		}
		api.WriteJSON(w, http.StatusOK, out)
	})
}
