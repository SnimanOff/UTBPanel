package users

import (
	"context"
	"time"
)

const (
	audienceWindow = 30
	regularDays    = 3
)

// AudienceActivity partitions users by current block status and recorded UTC activity days.
type AudienceActivity struct {
	Regular   int64 `cbor:"1,keyasint" json:"regular"`
	Recent    int64 `cbor:"2,keyasint" json:"recent"`
	OneDay    int64 `cbor:"3,keyasint" json:"one_day"`
	Inactive  int64 `cbor:"4,keyasint" json:"inactive"`
	Blocked   int64 `cbor:"5,keyasint" json:"blocked"`
	Untracked int64 `cbor:"6,keyasint" json:"untracked"`
}

func (t *Tracker) audienceActivity(ctx context.Context, botID int64, now time.Time, a *Audience) error {
	today := DayOf(now)
	firstDay := today - audienceWindow + 1
	cutoff := time.Unix(firstDay*86400, 0).UTC().Format(time.DateOnly)
	rows, err := t.DB.R.QueryContext(ctx, `SELECT COALESCE(g.is_premium, 0), b.blocked_at IS NOT NULL,
		b.updates_total > 0, substr(b.first_seen, 1, 10) = substr(b.last_seen, 1, 10), b.last_seen >= ?,
		b.active_days, b.active_anchor
		FROM bot_users b LEFT JOIN tg_users g ON g.user_id = b.user_id WHERE b.bot_id = ?`, cutoff, botID)
	if err != nil {
		return err
	}
	defer rows.Close()

	activity := &AudienceActivity{}
	for rows.Next() {
		var premium, blocked, tracked, oneDay, recent bool
		var bits []byte
		var anchor int64
		if err := rows.Scan(&premium, &blocked, &tracked, &oneDay, &recent, &bits, &anchor); err != nil {
			return err
		}
		a.Total++
		if premium {
			a.Premium++
		}
		switch {
		case blocked:
			activity.Blocked++
		case !tracked:
			activity.Untracked++
		case recent && isRegular(bits, anchor, firstDay, today):
			activity.Regular++
		case recent:
			activity.Recent++
		case oneDay:
			activity.OneDay++
		default:
			activity.Inactive++
		}
	}
	a.Activity = activity
	return rows.Err()
}

func isRegular(bits []byte, anchor, firstDay, today int64) bool {
	active := 0
	for day := min(today, anchor); day >= firstDay; day-- {
		if Active(bits, anchor, day) {
			active++
			if active == regularDays {
				return true
			}
		}
	}
	return false
}
