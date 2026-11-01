package metrics

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/axiomhq/hyperloglog"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// Retention of each table.
var (
	KeepMinutes = 30 * 24 * time.Hour
	KeepHours   = 365 * 24 * time.Hour
)

// Storage is the panel's history of metrics in panel.db.
type Storage struct {
	DB  *store.DB
	W   *store.Writer
	Log *slog.Logger
}

// Fetch is how the panel asks the core for finished minutes.
type Fetch func(ctx context.Context, after int64) ([]Minute, error)

// Ack confirms minutes up to an id were written.
type Ack func(ctx context.Context, upTo int64) error

// Save writes minutes in one transaction: rows of the same bot and minute
// are merged, and the minute's HyperLogLog sketches go into the day's.
func (s *Storage) Save(ctx context.Context, ms []Minute) error {
	if len(ms) == 0 {
		return nil
	}
	return s.W.Now(ctx, func(tx *sql.Tx) error {
		for i := range ms {
			m := &ms[i]
			var old []byte
			if err := tx.QueryRow(`SELECT data FROM m_minute WHERE bot_id = ? AND ts = ?`, m.BotID, m.TS).Scan(&old); err == nil {
				var prev Minute
				if Decode(old, &prev) == nil {
					prev.Merge(m)
					m = &Minute{BotID: m.BotID, TS: m.TS, Counters: prev.Counters, Hists: prev.Hists, Gauges: prev.Gauges, Uniq: m.Uniq}
				}
			}
			if _, err := tx.Exec(`INSERT INTO m_minute (bot_id, ts, data) VALUES (?, ?, ?)
				ON CONFLICT (bot_id, ts) DO UPDATE SET data = excluded.data`, m.BotID, m.TS, Encode(m)); err != nil {
				return err
			}
			day := m.TS / 86400
			for kind, sk := range m.Uniq {
				if err := mergeUniq(tx, m.BotID, day, kind, sk); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func mergeUniq(tx *sql.Tx, bot, day int64, kind string, sketch []byte) error {
	var add hyperloglog.Sketch
	if err := add.UnmarshalBinary(sketch); err != nil {
		return nil // a broken sketch is skipped, counts stay
	}
	var old []byte
	cur := hyperloglog.New14()
	if err := tx.QueryRow(`SELECT hll FROM m_uniq WHERE bot_id = ? AND day = ? AND kind = ?`, bot, day, kind).Scan(&old); err == nil {
		cur.UnmarshalBinary(old)
	}
	if err := cur.Merge(&add); err != nil {
		return err
	}
	b, _ := cur.MarshalBinary()
	_, err := tx.Exec(`INSERT INTO m_uniq (bot_id, day, kind, hll) VALUES (?, ?, ?, ?)
		ON CONFLICT (bot_id, day, kind) DO UPDATE SET hll = excluded.hll`, bot, day, kind, b)
	return err
}

// Pull fetches minutes from the core, writes them and confirms. The core
// deletes minutes only after the confirmation, so nothing is lost if the
// panel falls between the steps.
func (s *Storage) Pull(ctx context.Context, fetch Fetch, ack Ack) (int, error) {
	total := 0
	for {
		ms, err := fetch(ctx, 0)
		if err != nil || len(ms) == 0 {
			return total, err
		}
		if err := s.Save(ctx, ms); err != nil {
			return total, err
		}
		if err := ack(ctx, ms[len(ms)-1].ID); err != nil {
			return total, err
		}
		total += len(ms)
		if len(ms) < 5000 {
			return total, nil
		}
	}
}

// Rollup folds minutes into hours and hours into days for the last two
// hours/days (idempotent), then drops data past retention. Run hourly.
func (s *Storage) Rollup(ctx context.Context, now time.Time) error {
	hour := now.Unix() / 3600 * 3600
	for h := hour - 3600; h <= hour; h += 3600 {
		if err := s.fold(ctx, "m_minute", "ts", "m_hour", "ts", h, h+3600); err != nil {
			return err
		}
	}
	day := now.Unix() / 86400
	for d := day - 1; d <= day; d++ {
		if err := s.fold(ctx, "m_hour", "ts", "m_day", "day", d*86400, d*86400+86400); err != nil {
			return err
		}
	}
	return s.W.Now(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM m_minute WHERE ts < ?`, now.Add(-KeepMinutes).Unix()); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM m_server WHERE ts < ?`, now.Add(-KeepMinutes).Unix()); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM m_hour WHERE ts < ?`, now.Add(-KeepHours).Unix())
		return err
	})
}

// fold sums rows of src in [from, to) per bot into one row of dst.
func (s *Storage) fold(ctx context.Context, src, srcCol, dst, dstCol string, from, to int64) error {
	rows, err := s.DB.R.QueryContext(ctx, `SELECT bot_id, data FROM `+src+` WHERE `+srcCol+` >= ? AND `+srcCol+` < ?`, from, to)
	if err != nil {
		return err
	}
	sums := map[int64]*Minute{}
	for rows.Next() {
		var bot int64
		var data []byte
		if err := rows.Scan(&bot, &data); err != nil {
			rows.Close()
			return err
		}
		var m Minute
		if Decode(data, &m) != nil {
			continue
		}
		if sums[bot] == nil {
			sums[bot] = &Minute{BotID: bot}
		}
		sums[bot].Merge(&m)
	}
	rows.Close()
	if len(sums) == 0 {
		return nil
	}
	key := from
	if dstCol == "day" {
		key = from / 86400
	}
	return s.W.Now(ctx, func(tx *sql.Tx) error {
		for bot, m := range sums {
			if _, err := tx.Exec(`INSERT INTO `+dst+` (bot_id, `+dstCol+`, data) VALUES (?, ?, ?)
				ON CONFLICT DO UPDATE SET data = excluded.data`, bot, key, Encode(m)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Run pulls every 10 s and rolls up every hour until ctx ends.
func (s *Storage) Run(ctx context.Context, fetch Fetch, ack Ack) {
	pull := time.NewTicker(10 * time.Second)
	roll := time.NewTicker(time.Hour)
	defer pull.Stop()
	defer roll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pull.C:
			if _, err := s.Pull(ctx, fetch, ack); err != nil && ctx.Err() == nil {
				s.Log.Debug("metrics pull", "err", err)
			}
		case <-roll.C:
			if err := s.Rollup(ctx, time.Now()); err != nil && ctx.Err() == nil {
				s.Log.Warn("metrics rollup failed", "err", err)
			}
		}
	}
}
