package users

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/updrules"
)

// FloodSettings is settings.antiflood of a bot.
type FloodSettings struct {
	Enabled  bool   `json:"enabled" yaml:"enabled"`
	Limit    int    `json:"limit" yaml:"limit"`   // updates per window
	Window   string `json:"window" yaml:"window"` // "10s"
	Warn     bool   `json:"warn" yaml:"warn"`
	WarnText string `json:"warn_text" yaml:"warn_text"`
	Autoban  bool   `json:"autoban" yaml:"autoban"`
	Strikes  int    `json:"strikes" yaml:"strikes"`     // violations within an hour
	BanHours int    `json:"ban_hours" yaml:"ban_hours"` // length of the automatic ban
	window   time.Duration
}

// DefaultFlood: 20 updates in 10 s; a warning once per window; 3
// violations in an hour ban for a day.
func DefaultFlood() FloodSettings {
	return FloodSettings{Enabled: true, Limit: 20, Window: "10s", Warn: true,
		WarnText: "Слишком много сообщений, подождите немного", Autoban: true, Strikes: 3, BanHours: 24}
}

type floodUser struct {
	start   time.Time // window start
	n       int
	warned  bool
	strikes []time.Time
}

// Antiflood drops updates above the limit in the core: the bot does not
// write flood protection itself (step 10).
type Antiflood struct {
	DB   *sql.DB
	Bans *Bans
	Now  func() time.Time

	mu       sync.Mutex
	settings map[int64]*FloodSettings
	users    map[[2]int64]*floodUser
	lastGC   time.Time
}

func (a *Antiflood) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Antiflood) settingsOf(ctx context.Context, bot int64) *FloodSettings {
	if s := a.settings[bot]; s != nil {
		return s
	}
	s := DefaultFlood()
	var raw sql.NullString
	if a.DB != nil {
		a.DB.QueryRowContext(ctx, `SELECT json_extract(settings, '$.antiflood') FROM bots WHERE id = ?`, bot).Scan(&raw)
	}
	if raw.Valid {
		json.Unmarshal([]byte(raw.String), &s)
	}
	s.window, _ = time.ParseDuration(s.Window)
	if s.window <= 0 {
		s.window = 10 * time.Second
	}
	if s.Limit <= 0 {
		s.Limit = 20
	}
	if a.settings == nil {
		a.settings = map[int64]*FloodSettings{}
	}
	a.settings[bot] = &s
	return &s
}

// Invalidate re-reads settings of a bot on the next update.
func (a *Antiflood) Invalidate(bot int64) {
	a.mu.Lock()
	delete(a.settings, bot)
	a.mu.Unlock()
}

// Check is pipeline step 10. Payments and service updates are not counted.
func (a *Antiflood) Check(ctx context.Context, u *ingress.Update) ingress.Verdict {
	if u.UserID == 0 || u.Kind == updrules.KindPayment || u.Kind == updrules.KindMembership || u.Kind == updrules.KindPreCheckout {
		return ingress.Verdict{}
	}
	a.mu.Lock()
	s := a.settingsOf(ctx, u.BotID)
	if !s.Enabled {
		a.mu.Unlock()
		return ingress.Verdict{}
	}
	now := a.now()
	if a.users == nil {
		a.users = map[[2]int64]*floodUser{}
	}
	if now.Sub(a.lastGC) > time.Minute {
		for k, fu := range a.users {
			if now.Sub(fu.start) > time.Hour {
				delete(a.users, k)
			}
		}
		a.lastGC = now
	}
	k := [2]int64{u.BotID, u.UserID}
	fu := a.users[k]
	if fu == nil {
		fu = &floodUser{start: now}
		a.users[k] = fu
	}
	if now.Sub(fu.start) >= s.window {
		fu.start, fu.n, fu.warned = now, 0, false
	}
	fu.n++
	if fu.n <= s.Limit {
		a.mu.Unlock()
		return ingress.Verdict{}
	}
	// Over the limit. The first excess in a window is a violation.
	ban := false
	if fu.n == s.Limit+1 && s.Autoban {
		keep := fu.strikes[:0]
		for _, t := range fu.strikes {
			if now.Sub(t) < time.Hour {
				keep = append(keep, t)
			}
		}
		fu.strikes = append(keep, now)
		if len(fu.strikes) >= s.Strikes {
			ban = true
			fu.strikes = nil
		}
	}
	warn := s.Warn && !fu.warned
	fu.warned = true
	text, hours := s.WarnText, s.BanHours
	a.mu.Unlock()

	if ban && a.Bans != nil {
		a.Bans.Ban(context.WithoutCancel(ctx), u.BotID, u.UserID, "флуд", time.Duration(hours)*time.Hour, "антифлуд")
	}
	if !warn {
		return ingress.Verdict{Stop: true, Reason: updrules.Antiflood}
	}
	return ingress.Verdict{Stop: true, Reason: updrules.Antiflood, Text: text}
}
