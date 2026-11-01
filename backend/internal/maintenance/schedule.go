package maintenance

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Plan is a request for scheduled maintenance.
type Plan struct {
	BotID     int64     `cbor:"1,keyasint" json:"bot_id"`
	StartsAt  time.Time `cbor:"2,keyasint" json:"starts_at"`
	EndsAt    time.Time `cbor:"3,keyasint" json:"ends_at"`
	Text      string    `cbor:"4,keyasint" json:"text"`
	Version   string    `cbor:"5,keyasint" json:"version,omitempty"`
	AutoEnd   bool      `cbor:"6,keyasint" json:"auto_end"`
	WarnHours int       `cbor:"7,keyasint" json:"warn_hours,omitempty"`
}

// Errors of planning.
var (
	ErrTimes = errors.New("maintenance: the end must be after the start")
	ErrPast  = errors.New("maintenance: the end is already in the past")
)

// Schedule plans maintenance (a start in the past starts it at once).
func (s *Service) Schedule(ctx context.Context, p Plan) error {
	if !p.EndsAt.After(p.StartsAt) {
		return ErrTimes
	}
	if p.EndsAt.Before(time.Now()) {
		return ErrPast
	}
	if m, ok := s.Get(p.BotID); ok && m.State == Active {
		return ErrActive
	}
	return s.save(ctx, &M{BotID: p.BotID, Kind: Scheduled, State: Planned, StartsAt: p.StartsAt.UTC(), EndsAt: p.EndsAt.UTC(),
		Text: strings.TrimSpace(p.Text), Version: p.Version, AutoEnd: p.AutoEnd && p.Version != "", WarnHours: p.WarnHours})
}

// Extend moves the planned end; the text for users updates by itself.
func (s *Service) Extend(ctx context.Context, botID int64, d time.Duration) error {
	m, ok := s.Get(botID)
	if !ok || m.Kind != Scheduled {
		return errors.New("maintenance: nothing to extend")
	}
	base := m.EndsAt
	if base.Before(time.Now()) {
		base = time.Now().UTC()
	}
	m.EndsAt, m.Overdue = base.Add(d), false
	return s.save(ctx, &m)
}

// Hooks of the scheduler.
type Hooks struct {
	// Deploy switches the bot to a version at the start of maintenance.
	Deploy func(ctx context.Context, botID int64, version string) error
	// Event reports overdue maintenance and failed deploys.
	Event func(botID int64, typ string, data map[string]any)
	// Warn sends the early warning to active users (broadcasts).
	Warn func(ctx context.Context, m M, text string) error
}

// Tick starts due maintenance, runs its deploy and notices overdue ones.
func (s *Service) Tick(ctx context.Context, h Hooks, now time.Time) {
	s.mu.RLock()
	var list []M
	for _, m := range s.cache {
		list = append(list, *m)
	}
	s.mu.RUnlock()
	for _, m := range list {
		if m.Kind != Scheduled {
			continue
		}
		switch {
		case m.State == Planned && m.WarnHours > 0 && !m.Warned && h.Warn != nil &&
			now.After(m.StartsAt.Add(-time.Duration(m.WarnHours)*time.Hour)):
			text := "В {start} по Москве начнутся техработы, примерно до {end}."
			loc := time.UTC
			if s.TZ != nil {
				loc = s.TZ(ctx, m.BotID)
			}
			if h.Warn(ctx, m, Render(text, m.StartsAt, m.EndsAt, loc, now)) == nil {
				m.Warned = true
				s.save(ctx, &m)
			}
		case m.State == Planned && !now.Before(m.StartsAt):
			m.State = Active
			s.save(ctx, &m)
			if m.Version != "" && h.Deploy != nil {
				if err := h.Deploy(ctx, m.BotID, m.Version); err != nil {
					// Maintenance stays until the owner decides: end it on the
					// old version or extend it.
					h.Event(m.BotID, "maintenance_deploy_failed", map[string]any{"version": m.Version, "error": err.Error()})
					continue
				}
				if m.AutoEnd {
					s.End(ctx, m.BotID)
				}
			}
		case m.State == Active && !m.Overdue && now.After(m.EndsAt):
			m.Overdue = true
			s.save(ctx, &m)
			if h.Event != nil {
				h.Event(m.BotID, "maintenance_overdue", map[string]any{"ends_at": m.EndsAt, "extend": []int{15, 30, 60}})
			}
		}
	}
}

// Run ticks every 15 seconds until ctx ends.
func (s *Service) Run(ctx context.Context, h Hooks) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		s.Tick(ctx, h, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
