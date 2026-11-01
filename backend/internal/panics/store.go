package panics

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// FixedAfter: no repeat this long after a newer deploy → "maybe fixed".
var FixedAfter = 24 * time.Hour

// Group is a panic group shown in the "Паники" tab.
type Group struct {
	ID          int64     `json:"id"`
	Kind        string    `json:"kind"`
	Frame       string    `json:"frame"`
	Count       int64     `json:"count"`
	FirstAt     time.Time `json:"first_at"`
	LastAt      time.Time `json:"last_at"`
	Versions    []string  `json:"versions"`
	LastVersion string    `json:"last_version"`
	LastStack   string    `json:"last_stack"`
	Count24h    int64     `json:"count_24h"`
	MaybeFixed  bool      `json:"maybe_fixed"`
}

// Store keeps panic groups in panel.db.
type Store struct {
	DB  *store.DB
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// OnCoreEvent takes "panic" events of the core.
func (s *Store) OnCoreEvent(ev events.Event) {
	if ev.Type != "panic" {
		return
	}
	str := func(k string) string { v, _ := ev.Data[k].(string); return v }
	s.Add(context.Background(), Panic{BotID: ev.BotID, Time: ev.Time, Kind: str("kind"), Frame: str("frame"),
		Signature: str("signature"), Stack: str("stack"), Version: str("version")})
}

// Add counts one panic in its group.
func (s *Store) Add(ctx context.Context, p Panic) error {
	ts := p.Time.Unix()
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		var id int64
		var versions string
		err := tx.QueryRowContext(ctx, `SELECT id, versions FROM panic_groups WHERE bot_id = ? AND signature = ?`, p.BotID, p.Signature).Scan(&id, &versions)
		var vs []string
		json.Unmarshal([]byte(versions), &vs)
		if p.Version != "" && !slices.Contains(vs, p.Version) {
			vs = append(vs, p.Version)
		}
		vj, _ := json.Marshal(nonNil(vs))
		switch {
		case err == sql.ErrNoRows:
			res, err := tx.ExecContext(ctx, `INSERT INTO panic_groups (bot_id, signature, kind, frame, count, first_at, last_at, versions, last_version, last_stack)
				VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?)`, p.BotID, p.Signature, p.Kind, p.Frame, ts, ts, string(vj), p.Version, p.Stack)
			if err != nil {
				return err
			}
			id, _ = res.LastInsertId()
		case err != nil:
			return err
		default:
			if _, err := tx.ExecContext(ctx, `UPDATE panic_groups SET count = count + 1, last_at = MAX(last_at, ?), frame = ?, versions = ?, last_version = ?, last_stack = ? WHERE id = ?`,
				ts, p.Frame, string(vj), p.Version, p.Stack, id); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO panic_hits (group_id, bot_id, ts) VALUES (?, ?, ?)`, id, p.BotID, ts)
		return err
	})
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// Groups returns a bot's groups, the most recent first.
func (s *Store) Groups(ctx context.Context, botID int64) ([]Group, error) {
	now := s.now()
	var lastDeploy int64
	s.DB.R.QueryRowContext(ctx, `SELECT COALESCE(MAX(ts), 0) FROM deploys WHERE bot_id = ? AND result IN ('ok', 'observing')`, botID).Scan(&lastDeploy)
	rows, err := s.DB.R.QueryContext(ctx, `SELECT g.id, g.kind, g.frame, g.count, g.first_at, g.last_at, g.versions, g.last_version, g.last_stack,
		(SELECT COUNT(*) FROM panic_hits h WHERE h.group_id = g.id AND h.ts >= ?)
		FROM panic_groups g WHERE g.bot_id = ? ORDER BY g.last_at DESC LIMIT 200`, now.Add(-24*time.Hour).Unix(), botID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		var first, last int64
		var vs string
		if err := rows.Scan(&g.ID, &g.Kind, &g.Frame, &g.Count, &first, &last, &vs, &g.LastVersion, &g.LastStack, &g.Count24h); err != nil {
			return nil, err
		}
		g.FirstAt, g.LastAt = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		json.Unmarshal([]byte(vs), &g.Versions)
		g.Versions = nonNil(g.Versions)
		// A newer version ran a day without this panic.
		g.MaybeFixed = lastDeploy > last && now.Unix()-lastDeploy >= int64(FixedAfter.Seconds())
		out = append(out, g)
	}
	return out, rows.Err()
}

// Summary is the number of panics in 24 h per bot (badges).
func (s *Store) Summary(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT bot_id, COUNT(*) FROM panic_hits WHERE ts >= ? GROUP BY bot_id`, s.now().Add(-24*time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, n int64
		rows.Scan(&id, &n)
		out[id] = n
	}
	return out, rows.Err()
}

// Prune drops hits older than 30 days.
func (s *Store) Prune(ctx context.Context) {
	s.DB.W.ExecContext(ctx, `DELETE FROM panic_hits WHERE ts < ?`, s.now().Add(-30*24*time.Hour).Unix())
}

// Register adds the routes.
func (s *Store) Register(mux *api.Mux) {
	mux.HandleFunc("GET /api/bots/{id}/panics", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		gs, err := s.Groups(r.Context(), id)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		api.WriteJSON(w, http.StatusOK, gs)
	})
	mux.HandleFunc("GET /api/panics/summary", func(w http.ResponseWriter, r *http.Request) {
		m, err := s.Summary(r.Context())
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		out := map[string]int64{}
		for k, v := range m {
			out[strconv.FormatInt(k, 10)] = v
		}
		api.WriteJSON(w, http.StatusOK, out)
	})
}
