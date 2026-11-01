package sdkhost

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/counters"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	sdkmetrics "github.com/SnimanOff/UTBPanel/backend/sdk/metrics"
	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
)

type Metric struct {
	Name     string  `cbor:"1,keyasint" json:"name"`
	Kind     string  `cbor:"2,keyasint" json:"kind"`
	Counter  int64   `cbor:"3,keyasint" json:"counter"`
	Value    float64 `cbor:"4,keyasint" json:"value"`
	Selected bool    `cbor:"5,keyasint" json:"selected"`
}

type Metrics struct {
	DB         *store.DB
	Count      *counters.Counters
	LimitEvent func(int64, int64)
	mu         sync.Mutex
	warned     map[int64]time.Time
}

func (m *Metrics) Register(mux *wire.Mux, id int64, instance string) {
	mux.Handle("sdk.metrics", wire.Func(func(ctx context.Context, _ *wire.Conn, b sdkmetrics.Batch) (any, error) {
		return nil, m.Apply(ctx, id, instance, b)
	}))
}

func (m *Metrics) Apply(ctx context.Context, id int64, instance string, b sdkmetrics.Batch) error {
	if decoded, err := hex.DecodeString(b.Source); err != nil || len(decoded) != 16 || b.Sequence <= 0 || len(b.Points) > sdkmetrics.MaxNames {
		return fmt.Errorf("sdk: invalid metrics batch")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	accepted := []sdkmetrics.Point{}
	dropped := max(int64(0), b.Dropped)
	duplicate := false
	err := m.DB.Tx(ctx, func(tx *sql.Tx) error {
		var sequence int64
		err := tx.QueryRowContext(ctx, `SELECT sequence FROM sdk_metric_sources WHERE bot_id=? AND instance=? AND source=?`, id, instance, b.Source).Scan(&sequence)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if b.Sequence <= sequence {
			duplicate = true
			return nil
		}
		var names int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sdk_metrics WHERE bot_id=?`, id).Scan(&names); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, p := range b.Points {
			if !sdkmetrics.ValidName(p.Name) || (p.Kind != "counter" && p.Kind != "value") || p.Delta < 0 || math.IsNaN(p.Value) || math.IsInf(p.Value, 0) || seen[p.Name] {
				dropped++
				continue
			}
			seen[p.Name] = true
			var kind string
			var total int64
			err := tx.QueryRowContext(ctx, `SELECT kind,counter_value FROM sdk_metrics WHERE bot_id=? AND name=?`, id, p.Name).Scan(&kind, &total)
			if err == sql.ErrNoRows {
				if names >= sdkmetrics.MaxNames {
					dropped++
					continue
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO sdk_metrics(bot_id,name,kind) VALUES(?,?,?)`, id, p.Name, p.Kind); err != nil {
					return err
				}
				kind = p.Kind
				names++
			} else if err != nil {
				return err
			}
			if kind != p.Kind || p.Delta > math.MaxInt64-total {
				dropped++
				continue
			}
			if p.Kind == "counter" {
				_, err = tx.ExecContext(ctx, `UPDATE sdk_metrics SET counter_value=counter_value+? WHERE bot_id=? AND name=?`, p.Delta, id, p.Name)
			} else {
				_, err = tx.ExecContext(ctx, `UPDATE sdk_metrics SET gauge_value=? WHERE bot_id=? AND name=?`, p.Value, id, p.Name)
			}
			if err != nil {
				return err
			}
			accepted = append(accepted, p)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sdk_metric_sources(bot_id,instance,source,sequence,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(bot_id,instance,source) DO UPDATE SET sequence=excluded.sequence,updated_at=excluded.updated_at`, id, instance, b.Source, b.Sequence, time.Now().UnixNano())
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM sdk_metric_sources WHERE bot_id=? AND rowid IN (SELECT rowid FROM sdk_metric_sources WHERE bot_id=? ORDER BY updated_at DESC LIMIT -1 OFFSET 64)`, id, id)
		return err
	})
	if err != nil || duplicate {
		return err
	}
	for _, p := range accepted {
		if p.Kind == "counter" {
			m.Count.Add(id, "sdk_"+p.Name, p.Delta)
		} else {
			m.Count.SetGauge(id, "sdk_"+p.Name, p.Value)
		}
	}
	if dropped > 0 && m.LimitEvent != nil {
		if m.warned == nil {
			m.warned = map[int64]time.Time{}
		}
		if time.Since(m.warned[id]) >= time.Minute {
			m.warned[id] = time.Now()
			m.LimitEvent(id, dropped)
		}
	}
	return nil
}

func (m *Metrics) List(ctx context.Context, id int64) ([]Metric, error) {
	rows, err := m.DB.R.QueryContext(ctx, `SELECT name,kind,counter_value,gauge_value,selected FROM sdk_metrics WHERE bot_id=? ORDER BY name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Metric{}
	for rows.Next() {
		var v Metric
		if err := rows.Scan(&v.Name, &v.Kind, &v.Counter, &v.Value, &v.Selected); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (m *Metrics) Select(ctx context.Context, id int64, names []string) error {
	if len(names) > sdkmetrics.MaxNames {
		return fmt.Errorf("sdk: too many selected metrics")
	}
	return m.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE sdk_metrics SET selected=0 WHERE bot_id=?`, id); err != nil {
			return err
		}
		for _, name := range names {
			res, err := tx.ExecContext(ctx, `UPDATE sdk_metrics SET selected=1 WHERE bot_id=? AND name=?`, id, name)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("sdk: unknown metric %q", name)
			}
		}
		return nil
	})
}
