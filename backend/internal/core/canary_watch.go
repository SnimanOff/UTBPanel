package core

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
)

var canaryActor = journal.Actor{Source: journal.SourceAutorollback, Name: "canary"}

// canaryStats compare the canary with the main instance since its start.
type canaryStats struct {
	mu              sync.Mutex
	ok, bad         int
	mainOK, mainBad int
}

func (c *Core) canaryStatsOf(botID int64) *canaryStats {
	c.canaries.mu.Lock()
	defer c.canaries.mu.Unlock()
	if c.canaries.stats == nil {
		c.canaries.stats = map[int64]*canaryStats{}
	}
	s := c.canaries.stats[botID]
	if s == nil {
		s = &canaryStats{}
		c.canaries.stats[botID] = s
	}
	return s
}

func (c *Core) dropCanaryStats(botID int64) {
	c.canaries.mu.Lock()
	delete(c.canaries.stats, botID)
	c.canaries.mu.Unlock()
}

// observeCanary counts outcomes of bots that have a canary.
func (c *Core) observeCanary(o ingress.Outcome) {
	c.canaries.mu.RLock()
	s := c.canaries.stats[o.Update.BotID]
	c.canaries.mu.RUnlock()
	if s == nil {
		return
	}
	good := o.Result == "delivered" || o.Result == "replied"
	bad := o.Result == "error" || o.Result == "timeout"
	if !good && !bad {
		return
	}
	s.mu.Lock()
	switch {
	case o.Update.Instance == "canary" && good:
		s.ok++
	case o.Update.Instance == "canary":
		s.bad++
	case good:
		s.mainOK++
	default:
		s.mainBad++
	}
	s.mu.Unlock()
}

// CanaryVerdict: cancel when the canary breaks the autorollback rules,
// promote after autoAfter when it is no worse than the main instance.
func CanaryVerdict(r deploy.Rules, ok, bad, mainOK, mainBad int, age, autoAfter time.Duration) (string, string) {
	if n := ok + bad; n >= r.MinUpdates && float64(bad)/float64(n) > r.ErrShare {
		return "cancel", fmt.Sprintf("ошибок и таймаутов %d%% (%d из %d)", bad*100/n, bad, n)
	}
	if autoAfter > 0 && age >= autoAfter {
		share := func(b, n int) float64 {
			if n == 0 {
				return 0
			}
			return float64(b) / float64(n)
		}
		// "Not more errors": a small margin for noise.
		if share(bad, ok+bad) <= share(mainBad, mainOK+mainBad)+0.01 {
			return "promote", ""
		}
	}
	return "", ""
}

// watchCanaries checks running canaries every 5 seconds.
func (c *Core) watchCanaries(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rows, err := c.DB.R.QueryContext(ctx, `SELECT bot_id FROM canaries`)
		if err != nil {
			continue
		}
		var ids []int64
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			c.checkCanary(ctx, id)
		}
	}
}

func (c *Core) checkCanary(ctx context.Context, botID int64) {
	cn, err := c.CanaryOf(ctx, botID)
	if err != nil || cn == nil {
		return
	}
	rules := c.rules
	if rules.Window == 0 {
		rules = deploy.DefaultRules
	}
	s := c.canaryStatsOf(botID)
	s.mu.Lock()
	ok, bad, mok, mbad := s.ok, s.bad, s.mainOK, s.mainBad
	s.mu.Unlock()
	switch v, why := CanaryVerdict(rules, ok, bad, mok, mbad, time.Since(cn.StartedAt), time.Duration(cn.AutoAfter)*time.Minute); v {
	case "cancel":
		c.Journal.Do(ctx, canaryActor, "canary.stop", botID, map[string]string{"reason": why}, func() error {
			return c.StopCanary(ctx, botID, "автоотмена: "+why)
		})
		c.Ev.Emit("canary_cancelled", botID, events.Error, map[string]any{"version": cn.Version, "reason": why})
	case "promote":
		c.Journal.Do(ctx, canaryActor, "canary.promote", botID, map[string]string{"version": cn.Version}, func() error {
			return c.PromoteCanary(ctx, botID, canaryActor)
		})
	}
}

// PromoteCanary moves everyone to the canary's version: the canary stops,
// the main instance is deployed with its version and watched as usual.
func (c *Core) PromoteCanary(ctx context.Context, botID int64, actor journal.Actor) error {
	cn, err := c.CanaryOf(ctx, botID)
	if err != nil {
		return err
	}
	if cn == nil {
		return errNoCanary
	}
	if err := c.StopCanary(ctx, botID, "переведены все"); err != nil {
		return err
	}
	c.Ev.Emit("canary", botID, events.Info, map[string]any{"action": "promoted", "version": cn.Version})
	_, err = c.Deploy(ctx, SwitchVersion{BotID: botID, Version: cn.Version, Actor: actor})
	return err
}

// canaryLabel is shown on cards: "канарейка v2 · 10 %".
func (c *Core) canaryLabel(ctx context.Context, botID int64) string {
	cn, err := c.CanaryOf(ctx, botID)
	if err != nil || cn == nil {
		return ""
	}
	l := "канарейка " + cn.Version + " · " + strconv.Itoa(cn.Share) + " %"
	if len(cn.Users) > 0 {
		l += " +" + strconv.Itoa(len(cn.Users))
	}
	return l
}
