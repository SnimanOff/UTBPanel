// Package users keeps users of bots in core.db: the core sees every update,
// so the bot does not need to. Updates are gathered in memory and written
// every 10 s in one transaction.
package users

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

type key struct{ bot, user int64 }

// pending is what changed for one user since the last write.
type pending struct {
	first, last time.Time
	updates     int64
	days        map[int64]bool
	source      string
	started     bool
	username    string
	language    string
	premium     bool
	hasProfile  bool
	// Block state from my_chat_member in a private chat; the last one wins.
	blockedAt, unblockedAt time.Time
}

// Tracker records users.
type Tracker struct {
	DB  *store.DB
	W   *store.Writer
	Log *slog.Logger
	// Every is the write interval (10 s).
	Every time.Duration
	// OnNew is called once for every user new to a bot (counters).
	OnNew func(botID, userID int64, source string)
	// OnBlock is called for every block (true) and unblock (false).
	OnBlock func(botID, userID int64, blocked bool)
	// OnPayment is called for every stored payment or refund.
	OnPayment func(botID int64, p ingress.Payment)

	mu   sync.Mutex
	pend map[key]*pending
}

// Account is the pipeline step: it records the sender of an update.
func (t *Tracker) Account(_ context.Context, u *ingress.Update) {
	if u.UserID == 0 {
		return
	}
	now := u.Received
	if now.IsZero() {
		now = time.Now()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pend == nil {
		t.pend = map[key]*pending{}
	}
	k := key{u.BotID, u.UserID}
	p := t.pend[k]
	if p == nil {
		p = &pending{first: now, days: map[int64]bool{}}
		t.pend[k] = p
	}
	p.last = now
	p.updates++
	p.days[DayOf(now)] = true
	switch u.MemberStatus {
	case "kicked":
		p.blockedAt = now
	case "member":
		p.unblockedAt = now
	}
	if u.Start {
		if !p.started {
			p.started, p.source = true, u.StartParam
		}
	}
	if u.Username != "" || u.Language != "" || u.Premium {
		p.username, p.language, p.premium, p.hasProfile = u.Username, u.Language, u.Premium, true
	}
}

// Run writes gathered changes every interval until ctx ends, then once more.
func (t *Tracker) Run(ctx context.Context) {
	every := t.Every
	if every <= 0 {
		every = 10 * time.Second
	}
	tk := time.NewTicker(every)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Flush(context.Background())
			return
		case <-tk.C:
			if err := t.Flush(ctx); err != nil {
				t.Log.Error("writing users failed", "err", err)
			}
		}
	}
}

// Flush writes everything gathered so far in one transaction.
func (t *Tracker) Flush(ctx context.Context) error {
	t.mu.Lock()
	batch := t.pend
	t.pend = nil
	t.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	var fresh []struct {
		k      key
		source string
	}
	err := t.W.Now(ctx, func(tx *sql.Tx) error {
		fresh = fresh[:0]
		for k, p := range batch {
			if p.hasProfile {
				if _, err := tx.Exec(`INSERT INTO tg_users (user_id, username, language_code, is_premium, updated_at) VALUES (?, ?, ?, ?, ?)
					ON CONFLICT (user_id) DO UPDATE SET username = excluded.username, language_code = excluded.language_code,
					is_premium = excluded.is_premium, updated_at = excluded.updated_at`,
					k.user, p.username, p.language, p.premium, store.FormatTime(p.last)); err != nil {
					return err
				}
			}
			var bits []byte
			var anchor int64
			var started bool
			err := tx.QueryRow(`SELECT active_days, active_anchor, started FROM bot_users WHERE bot_id = ? AND user_id = ?`, k.bot, k.user).
				Scan(&bits, &anchor, &started)
			isNew := err == sql.ErrNoRows
			if err != nil && !isNew {
				return err
			}
			for d := range p.days {
				bits, anchor = Mark(bits, anchor, d)
			}
			if isNew {
				if _, err := tx.Exec(`INSERT INTO bot_users (bot_id, user_id, first_seen, last_seen, source, started, updates_total, active_days, active_anchor)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, k.bot, k.user, store.FormatTime(p.first), store.FormatTime(p.last),
					p.source, p.started, p.updates, bits, anchor); err != nil {
					return err
				}
				if err := writeBlock(tx, k, p); err != nil {
					return err
				}
				fresh = append(fresh, struct {
					k      key
					source string
				}{k, p.source})
				continue
			}
			if err := writeBlock(tx, k, p); err != nil {
				return err
			}
			// The source is remembered only from the first /start.
			setSource := p.started && !started
			if _, err := tx.Exec(`UPDATE bot_users SET last_seen = MAX(last_seen, ?), updates_total = updates_total + ?,
				active_days = ?, active_anchor = ?,
				source = CASE WHEN ? THEN ? ELSE source END, started = started OR ?
				WHERE bot_id = ? AND user_id = ?`,
				store.FormatTime(p.last), p.updates, bits, anchor, setSource, p.source, p.started, k.bot, k.user); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// Put the batch back: nothing is lost on a failed write.
		t.mu.Lock()
		if t.pend == nil {
			t.pend = batch
		} else {
			for k, p := range batch {
				if _, ok := t.pend[k]; !ok {
					t.pend[k] = p
				}
			}
		}
		t.mu.Unlock()
		return err
	}
	if t.OnBlock != nil {
		for k, p := range batch {
			if !p.blockedAt.IsZero() || !p.unblockedAt.IsZero() {
				t.OnBlock(k.bot, k.user, p.blockedAt.After(p.unblockedAt))
			}
		}
	}
	if t.OnNew != nil {
		for _, f := range fresh {
			t.OnNew(f.k.bot, f.k.user, f.source)
		}
	}
	return nil
}

// Retention returns the share (0..1) of users who first came between
// cohortFrom and cohortTo days ago and were active n days after coming.
func (t *Tracker) Retention(ctx context.Context, botID int64, n int, now time.Time) (float64, int, error) {
	today := DayOf(now)
	from := time.Unix((today-int64(Days)+int64(n))*86400, 0).UTC()
	to := time.Unix((today-int64(n))*86400, 0).UTC()
	rows, err := t.DB.R.QueryContext(ctx, `SELECT first_seen, active_days, active_anchor FROM bot_users
		WHERE bot_id = ? AND first_seen >= ? AND first_seen < ?`, botID, store.FormatTime(from), store.FormatTime(to.Add(24*time.Hour)))
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var cohort, kept int
	for rows.Next() {
		var fs string
		var bits []byte
		var anchor int64
		if err := rows.Scan(&fs, &bits, &anchor); err != nil {
			return 0, 0, err
		}
		ft, _ := store.ParseTime(fs)
		target := DayOf(ft) + int64(n)
		if target > today {
			continue
		}
		cohort++
		if Active(bits, anchor, target) {
			kept++
		}
	}
	if cohort == 0 {
		return 0, 0, rows.Err()
	}
	return float64(kept) / float64(cohort), cohort, rows.Err()
}

// writeBlock stores the block state: blocked users leave broadcasts.
func writeBlock(tx *sql.Tx, k key, p *pending) error {
	if !p.blockedAt.IsZero() && p.blockedAt.After(p.unblockedAt) {
		_, err := tx.Exec(`UPDATE bot_users SET blocked_at = ? WHERE bot_id = ? AND user_id = ?`, store.FormatTime(p.blockedAt), k.bot, k.user)
		return err
	}
	if !p.unblockedAt.IsZero() {
		_, err := tx.Exec(`UPDATE bot_users SET blocked_at = NULL, unblocked_at = ? WHERE bot_id = ? AND user_id = ?`, store.FormatTime(p.unblockedAt), k.bot, k.user)
		return err
	}
	return nil
}

// Payments is the pipeline step for successful_payment and
// refunded_payment. Payments are written at once: money is never batched.
func (t *Tracker) Payments(ctx context.Context, u *ingress.Update) {
	if u.Payment == nil {
		return
	}
	p := *u.Payment
	at := store.FormatTime(u.Received)
	err := t.W.Now(ctx, func(tx *sql.Tx) error {
		if p.Refund {
			res, err := tx.Exec(`UPDATE payments SET refunded_at = ? WHERE bot_id = ? AND charge_id = ? AND charge_id != ''`, at, u.BotID, p.ChargeID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				return nil
			}
		}
		_, err := tx.Exec(`INSERT INTO payments (bot_id, ts, user_id, currency, amount, payload, charge_id, refunded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			u.BotID, at, u.UserID, p.Currency, p.Amount, p.Payload, p.ChargeID, refundAt(p, at))
		return err
	})
	if err != nil {
		t.Log.Error("payment not stored", "bot", u.BotID, "user", u.UserID, "err", err)
		return
	}
	if t.OnPayment != nil {
		t.OnPayment(u.BotID, p)
	}
}

func refundAt(p ingress.Payment, at string) any {
	if p.Refund {
		return at
	}
	return nil
}
