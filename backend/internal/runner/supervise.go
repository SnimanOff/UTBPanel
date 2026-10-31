package runner

import (
	"context"
	"sync"
	"time"
)

// Restart policy after a crash (variables so tests can shorten them).
var (
	backoffMin    = time.Second
	backoffMax    = time.Minute
	stableAfter   = 10 * time.Minute // a bot this long alive starts the pause over
	loopWindow    = 5 * time.Minute
	loopThreshold = 5 // crashes within loopWindow → state "error"
)

// supervisor keeps the crash history of one bot.
type supervisor struct {
	mu       sync.Mutex
	crashes  []time.Time
	backoff  time.Duration
	restarts int
	lastExit int
	lastAt   time.Time
	timer    *time.Timer
}

// Status is the supervision state of a bot.
type Status struct {
	Restarts   int       `cbor:"1,keyasint" json:"restarts"`
	LastExit   int       `cbor:"2,keyasint" json:"last_exit"`
	LastExitAt time.Time `cbor:"3,keyasint" json:"last_exit_at,omitzero"`
	Running    bool      `cbor:"4,keyasint" json:"running"`
	PID        int       `cbor:"5,keyasint" json:"pid,omitempty"`
	Started    time.Time `cbor:"6,keyasint" json:"started,omitzero"`
}

func (r *Runner) sup(id int64) *supervisor {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sups == nil {
		r.sups = map[int64]*supervisor{}
	}
	s := r.sups[id]
	if s == nil {
		s = &supervisor{backoff: backoffMin}
		r.sups[id] = s
	}
	return s
}

// Status returns restarts, last exit and the running process of a bot.
func (r *Runner) Status(id int64) Status {
	s := r.sup(id)
	s.mu.Lock()
	st := Status{Restarts: s.restarts, LastExit: s.lastExit, LastExitAt: s.lastAt}
	s.mu.Unlock()
	if info, ok := r.Info(id); ok {
		st.Running, st.PID, st.Started = true, info.PID, info.Started
	}
	return st
}

// inLoop reports whether the bot crashed loopThreshold times recently.
func (r *Runner) inLoop(id int64) bool {
	s := r.sup(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, t := range s.crashes {
		if time.Since(t) < loopWindow {
			n++
		}
	}
	return n >= loopThreshold
}

// cancelRestart drops a pending restart (the owner stopped the bot).
func (r *Runner) cancelRestart(id int64) {
	s := r.sup(id)
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.mu.Unlock()
}

// crashed schedules a restart with a growing pause: 1, 2, 4… s up to a
// minute. Five crashes in five minutes put the bot into the "error" state;
// restarts go on once a minute.
func (r *Runner) crashed(p *proc, code int) {
	id := p.bot.ID
	s := r.sup(id)
	now := time.Now()
	s.mu.Lock()
	if now.Sub(p.started) >= stableAfter {
		s.backoff = backoffMin
	}
	s.lastExit, s.lastAt = code, now
	keep := s.crashes[:0]
	for _, t := range s.crashes {
		if now.Sub(t) < loopWindow {
			keep = append(keep, t)
		}
	}
	s.crashes = append(keep, now)
	loop := len(s.crashes) >= loopThreshold
	delay := s.backoff
	if loop {
		delay = backoffMax
	}
	s.backoff = min(s.backoff*2, backoffMax)
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(delay, func() { r.restartAfterCrash(id) })
	s.mu.Unlock()

	if loop {
		r.setState(context.Background(), id, Errored)
		r.emit(id, "bot_crash_loop", "error", map[string]any{"crashes": len(s.crashes), "window_s": int(loopWindow.Seconds())})
	} else {
		r.setState(context.Background(), id, Crashed)
	}
	r.coreLine(id, "restarting in %v", delay)
}

func (r *Runner) restartAfterCrash(id int64) {
	s := r.sup(id)
	s.mu.Lock()
	s.timer = nil
	s.restarts++
	s.mu.Unlock()
	if r.IsRunning(id) || r.cfg.DB == nil {
		return
	}
	ctx := context.Background()
	b, err := LoadBot(ctx, r.cfg.DB, id)
	if err != nil {
		return // deleted meanwhile
	}
	var state string
	r.cfg.DB.R.QueryRowContext(ctx, `SELECT state FROM bots WHERE id = ?`, id).Scan(&state)
	if state == Stopped {
		return
	}
	r.emit(id, "bot_restarted", "warn", map[string]any{"restarts": s.restarts})
	if err := r.Start(ctx, b); err != nil {
		r.coreLine(id, "restart failed: %v", err)
	}
}

// Restore brings every bot back to its saved state after the core starts:
// running, crashed, erroring and paused bots are started (a paused bot's
// process runs, updates just do not reach it), stopped ones stay stopped.
func (r *Runner) Restore(ctx context.Context) {
	rows, err := r.cfg.DB.R.QueryContext(ctx, `SELECT id FROM bots WHERE state != 'stopped' ORDER BY id`)
	if err != nil {
		r.log.Error("restoring bots failed", "err", err)
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			b, err := LoadBot(ctx, r.cfg.DB, id)
			if err != nil {
				return
			}
			if err := r.Start(ctx, b); err != nil {
				r.log.Warn("bot did not start after core restart", "bot", id, "err", err)
				r.setState(ctx, id, Crashed)
				// Treat it as a crash so the usual restarts apply.
				r.crashed(&proc{bot: b, started: time.Now()}, -1)
			}
		})
	}
	wg.Wait()
	if len(ids) > 0 {
		r.log.Info("bots restored", "count", len(ids))
	}
}
