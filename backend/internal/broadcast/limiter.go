package broadcast

import (
	"context"
	"sync"
	"time"
)

// Limiter shares Telegram's ~30 messages per second of a bot between the
// bot itself and broadcasts. The bot's own calls (seen by tgproxy) always
// count and are never delayed; a broadcast message waits until the last
// second has room.
type Limiter struct {
	// Total is the bot and broadcasts together per second (28).
	Total int
	// Share is the most a broadcast gets per second when the bot is quiet (25).
	Share int

	mu   sync.Mutex
	bots map[int64]*window
	now  func() time.Time
}

type window struct {
	own, ours   []time.Time // within the last second
	pausedUntil time.Time   // after a 429
}

// NewLimiter returns the limiter with the defaults.
func NewLimiter() *Limiter { return &Limiter{Total: 28, Share: 25} }

func (l *Limiter) win(botID int64) *window {
	if l.bots == nil {
		l.bots = map[int64]*window{}
	}
	w := l.bots[botID]
	if w == nil {
		w = &window{}
		l.bots[botID] = w
	}
	return w
}

func (l *Limiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func trim(ts []time.Time, since time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(since) {
		i++
	}
	return ts[i:]
}

// Note records one call of the bot itself.
func (l *Limiter) Note(botID int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.win(botID)
	now := l.clock()
	w.own = append(trim(w.own, now.Add(-time.Second)), now)
}

// Pause stops broadcasts of a bot after Telegram's 429.
func (l *Limiter) Pause(botID int64, d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.win(botID)
	if until := l.clock().Add(d); until.After(w.pausedUntil) {
		w.pausedUntil = until
	}
}

// reserve takes a slot or tells how long to wait.
func (l *Limiter) reserve(botID int64) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.win(botID)
	now := l.clock()
	if now.Before(w.pausedUntil) {
		return w.pausedUntil.Sub(now)
	}
	since := now.Add(-time.Second)
	w.own, w.ours = trim(w.own, since), trim(w.ours, since)
	if len(w.ours) < l.Share && len(w.own)+len(w.ours) < l.Total {
		w.ours = append(w.ours, now)
		return 0
	}
	// Wait until the oldest entry leaves the window.
	oldest := now
	if len(w.ours) > 0 {
		oldest = w.ours[0]
	}
	if len(w.own) > 0 && w.own[0].Before(oldest) && len(w.ours) < l.Share {
		oldest = w.own[0]
	}
	d := oldest.Add(time.Second).Sub(now)
	if d < time.Millisecond {
		d = time.Millisecond
	}
	return d
}

// Wait blocks until a broadcast message of the bot may go.
func (l *Limiter) Wait(ctx context.Context, botID int64) error {
	for {
		d := l.reserve(botID)
		if d == 0 {
			return nil
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}
