package deploy

import (
	"fmt"
	"time"
)

// Rules of the observation after a deploy.
type Rules struct {
	Window     time.Duration // 60 s
	ReadyAfter time.Duration // the bot must be up within 15 s
	MaxCrashes int           // 2 crashes and more → rollback
	ErrShare   float64       // share of errors and timeouts, 0.2
	MinUpdates int           // the share counts from this many updates
}

// DefaultRules are used unless a bot sets its own.
var DefaultRules = Rules{Window: time.Minute, ReadyAfter: 15 * time.Second, MaxCrashes: 2, ErrShare: 0.2, MinUpdates: 10}

// Stats are what the core saw since the deploy.
type Stats struct {
	Elapsed time.Duration
	Crashes int
	Running bool
	OK, Bad int // delivered vs. errors and timeouts
}

// Verdicts of Judge.
const (
	Watching = ""
	Passed   = "ok"
	Rollback = "rollback"
)

// Judge decides: keep watching, passed, or roll back (with the reason).
func (r Rules) Judge(s Stats) (string, string) {
	if s.Crashes >= r.MaxCrashes {
		return Rollback, fmt.Sprintf("%d падения за %s", s.Crashes, r.Window)
	}
	if !s.Running && s.Elapsed >= r.ReadyAfter {
		return Rollback, fmt.Sprintf("бот не готов через %s", r.ReadyAfter)
	}
	if n := s.OK + s.Bad; n >= r.MinUpdates && float64(s.Bad)/float64(n) > r.ErrShare {
		return Rollback, fmt.Sprintf("ошибок и таймаутов %d%% (%d из %d)", s.Bad*100/n, s.Bad, n)
	}
	if s.Elapsed >= r.Window {
		return Passed, ""
	}
	return Watching, ""
}
