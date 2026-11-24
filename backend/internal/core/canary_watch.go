package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

var canaryActor = journal.Actor{Source: journal.SourceCore, Name: "canary"}

// canaryStats compare the canary with the main instance since its start.
type canaryStats struct {
	mu                sync.Mutex
	ok, bad           int
	mainOK, mainBad   int
	needsConfirmation bool // protected by the per-bot deployment lock
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

// CanaryVerdict waits for a minimum sample before judging the canary.
// It cancels when errors exceed the threshold, or promotes after autoAfter
// when the canary has successful outcomes and its error rate is comparable
// to the main instance's or better.
func CanaryVerdict(r deploy.Rules, ok, bad, mainOK, mainBad int, age, autoAfter time.Duration) (string, string) {
	n := ok + bad
	enoughData := n >= max(1, r.MinUpdates)
	if enoughData && float64(bad)/float64(n) > r.ErrShare {
		return "cancel", fmt.Sprintf("ошибок и таймаутов %d%% (%d из %d)", bad*100/n, bad, n)
	}
	if enoughData && ok > 0 && autoAfter > 0 && age >= autoAfter {
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
	ctx, unlock, err := c.lockDeploy(ctx, botID)
	if err != nil {
		return
	}
	defer unlock()
	cn, err := c.CanaryOf(ctx, botID)
	if err != nil || cn == nil || !cn.Running {
		return
	}
	rules, autorollback := c.deployRules(ctx, botID)
	s := c.canaryStatsOf(botID)
	s.mu.Lock()
	ok, bad, mok, mbad := s.ok, s.bad, s.mainOK, s.mainBad
	s.mu.Unlock()
	switch v, why := CanaryVerdict(rules, ok, bad, mok, mbad, time.Since(cn.StartedAt), time.Duration(cn.AutoAfter)*time.Minute); v {
	case "cancel":
		if !autorollback {
			return
		}
		c.Journal.Do(ctx, canaryActor, "canary.stop", botID, map[string]string{"reason": why}, func() error {
			return c.StopCanary(ctx, botID, "автоотмена: "+why)
		})
		c.Ev.Emit("canary_cancelled", botID, events.Error, map[string]any{"version": cn.Version, "reason": why})
	case "promote":
		in := SwitchVersion{BotID: botID, Version: cn.Version, Actor: canaryActor}
		c.autoPromoteCanary(ctx, s, in, func() error {
			return c.PromoteCanary(ctx, botID, canaryActor)
		})
	}
}

func (c *Core) autoPromoteCanary(ctx context.Context, stats *canaryStats, in SwitchVersion, promote func() error) {
	needsConfirmation := func(err error) bool {
		var failure *wire.Error
		return errors.As(err, &failure) && failure.Code == "config_confirm"
	}
	// The first attempt reports that confirmation is needed. Further
	// checks are quiet until the configuration can pass validation.
	if stats.needsConfirmation && needsConfirmation(c.releaseConfig(ctx, in, false)) {
		return
	}
	err := c.Journal.Do(ctx, in.Actor, "canary.promote", in.BotID, map[string]string{"version": in.Version}, promote)
	stats.needsConfirmation = needsConfirmation(err)
}

// PromoteCanary moves everyone to the canary's version: the canary stops,
// the main instance is deployed with its version and watched as usual.
func (c *Core) PromoteCanary(ctx context.Context, botID int64, actor journal.Actor) error {
	ctx, unlock, err := c.lockDeploy(ctx, botID)
	if err != nil {
		return err
	}
	defer unlock()
	cn, err := c.CanaryOf(ctx, botID)
	if err != nil {
		return err
	}
	if cn == nil {
		return errNoCanary
	}
	if actor == canaryActor && !cn.Running {
		return wire.Errorf("conflict", "канарейка остановилась до автоперевода")
	}
	// Deploy checks the release before stopping the main process, whose
	// stop hook stops the canary. A rejected release keeps the canary.
	if _, err := c.Deploy(ctx, SwitchVersion{BotID: botID, Version: cn.Version, Actor: actor}); err != nil {
		return err
	}
	if err := c.StopCanary(ctx, botID, "переведены все"); err != nil {
		return err
	}
	c.Ev.Emit("canary", botID, events.Info, map[string]any{"action": "promoted", "version": cn.Version})
	return nil
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
