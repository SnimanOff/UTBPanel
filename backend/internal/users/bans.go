package users

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/updrules"
)

// Bans are per bot. A banned user's updates stop in the core with 200 and
// no answer (Telegram does not retry), except payments and service updates:
// the bot must still learn about money and membership.
type Bans struct {
	DB *store.DB

	mu    sync.RWMutex
	until map[int64]map[int64]time.Time // bot → user → end (zero = forever)
}

// ErrReason: a ban needs a reason.
var ErrReason = errors.New("users: a ban needs a reason")

// Load reads active bans into memory.
func (b *Bans) Load(ctx context.Context) error {
	rows, err := b.DB.R.QueryContext(ctx, `SELECT bot_id, user_id, ban_until FROM bot_users WHERE banned`)
	if err != nil {
		return err
	}
	defer rows.Close()
	m := map[int64]map[int64]time.Time{}
	for rows.Next() {
		var bot, user int64
		var until sql.NullString
		if err := rows.Scan(&bot, &user, &until); err != nil {
			return err
		}
		if m[bot] == nil {
			m[bot] = map[int64]time.Time{}
		}
		m[bot][user] = timeOrZero(until)
	}
	b.mu.Lock()
	b.until = m
	b.mu.Unlock()
	return rows.Err()
}

// Banned reports whether a user is banned in a bot right now.
func (b *Bans) Banned(bot, user int64) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	end, ok := b.until[bot][user]
	return ok && (end.IsZero() || time.Now().Before(end))
}

// Ban bans a user; for 0 duration forever. by is the key name or "антифлуд".
func (b *Bans) Ban(ctx context.Context, bot, user int64, reason string, d time.Duration, by string) error {
	if strings.TrimSpace(reason) == "" {
		return ErrReason
	}
	now := time.Now().UTC()
	var until any
	var end time.Time
	if d > 0 {
		end = now.Add(d)
		until = store.FormatTime(end)
	}
	_, err := b.DB.W.ExecContext(ctx, `INSERT INTO bot_users (bot_id, user_id, first_seen, last_seen, banned, ban_reason, ban_until, banned_at, banned_by)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?)
		ON CONFLICT (bot_id, user_id) DO UPDATE SET banned = 1, ban_reason = excluded.ban_reason, ban_until = excluded.ban_until,
		banned_at = excluded.banned_at, banned_by = excluded.banned_by`,
		bot, user, store.FormatTime(now), store.FormatTime(now), reason, until, store.FormatTime(now), by)
	if err != nil {
		return err
	}
	b.mu.Lock()
	if b.until == nil {
		b.until = map[int64]map[int64]time.Time{}
	}
	if b.until[bot] == nil {
		b.until[bot] = map[int64]time.Time{}
	}
	b.until[bot][user] = end
	b.mu.Unlock()
	return nil
}

// Unban lifts a ban.
func (b *Bans) Unban(ctx context.Context, bot, user int64) error {
	_, err := b.DB.W.ExecContext(ctx, `UPDATE bot_users SET banned = 0, ban_reason = '', ban_until = NULL WHERE bot_id = ? AND user_id = ?`, bot, user)
	if err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.until[bot], user)
	b.mu.Unlock()
	return nil
}

// Expire lifts bans whose time is over; returns how many.
func (b *Bans) Expire(ctx context.Context) (int, error) {
	now := store.Now()
	res, err := b.DB.W.ExecContext(ctx, `UPDATE bot_users SET banned = 0, ban_reason = '', ban_until = NULL
		WHERE banned AND ban_until IS NOT NULL AND ban_until <= ?`, now)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return int(n), b.Load(ctx)
	}
	return 0, nil
}

// Run lifts expired bans every minute.
func (b *Bans) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.Expire(ctx)
		}
	}
}

// Check is pipeline step 7.
func (b *Bans) Check(_ context.Context, u *ingress.Update) ingress.Verdict {
	if u.UserID == 0 || !b.Banned(u.BotID, u.UserID) {
		return ingress.Verdict{}
	}
	if u.Kind == updrules.KindPayment || u.Kind == updrules.KindMembership {
		return ingress.Verdict{}
	}
	return ingress.Verdict{Stop: true, Silent: true, Reason: updrules.Reason(-1)}
}
