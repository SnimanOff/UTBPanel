package core

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/tgproxy"
)

// Service statistics: the proxy does not decrypt other services, but sees
// where a bot connects (host and port from CONNECT).
var (
	MaxServices      = 50 // per bot; the rest is "other"
	ServiceWindow    = 5 * time.Minute
	ServiceErrorRate = 0.2
)

type svcSample struct {
	at   time.Time
	bot  int64
	fail bool
}

type services struct {
	mu      sync.Mutex
	perBot  map[int64]map[string]bool
	window  map[string][]svcSample
	down    map[string]bool
	seen    map[int64]map[string]bool
	onFirst func(botID int64, host string)
}

func (s *services) name(bot int64, host string) string {
	if s.perBot[bot] == nil {
		s.perBot[bot] = map[string]bool{}
	}
	if s.perBot[bot][host] {
		return host
	}
	if len(s.perBot[bot]) >= MaxServices {
		return "other"
	}
	s.perBot[bot][host] = true
	return host
}

// onConn counts a connection to another service.
func (c *Core) onConn(cn tgproxy.Conn) {
	s := &c.svc
	s.mu.Lock()
	host := s.name(cn.BotID, cn.Host)
	first := false
	if s.seen[cn.BotID] == nil {
		s.seen[cn.BotID] = map[string]bool{}
	}
	if !s.seen[cn.BotID][cn.Host] {
		s.seen[cn.BotID][cn.Host] = true
		first = true
	}
	if !cn.Done || cn.Err != "" {
		s.window[host] = append(s.window[host], svcSample{at: time.Now(), bot: cn.BotID, fail: cn.Err != ""})
	}
	s.mu.Unlock()
	if first && s.onFirst != nil {
		s.onFirst(cn.BotID, cn.Host)
	}
	p := "svc_" + host + "_"
	if !cn.Done || cn.Err != "" { // the opening of a connection (or its failure)
		c.Count.Add(cn.BotID, p+"conn", 1)
		if cn.Err != "" {
			c.Count.Add(cn.BotID, p+"err", 1)
			c.Count.Add(cn.BotID, p+"err_"+cn.Err, 1)
		} else {
			c.Count.Latency(cn.BotID, "svc_"+host, cn.Connect)
		}
	}
	if cn.Done {
		c.Count.Add(cn.BotID, p+"in", cn.BytesIn)
		c.Count.Add(cn.BotID, p+"out", cn.BytesOut)
	}
}

// watchServices raises one event per failing service (with the affected
// bots) and another when it is back.
func (c *Core) watchServices(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.checkServices(time.Now())
	}
}

func (c *Core) checkServices(now time.Time) {
	s := &c.svc
	type verdict struct {
		host  string
		rate  float64
		bots  []int64
		total int
	}
	var down, up []verdict
	s.mu.Lock()
	for host, samples := range s.window {
		keep := samples[:0]
		for _, x := range samples {
			if now.Sub(x.at) < ServiceWindow {
				keep = append(keep, x)
			}
		}
		s.window[host] = keep
		if len(keep) == 0 {
			if s.down[host] {
				delete(s.down, host)
				up = append(up, verdict{host: host})
			}
			continue
		}
		fails := 0
		bots := map[int64]bool{}
		for _, x := range keep {
			if x.fail {
				fails++
				bots[x.bot] = true
			}
		}
		rate := float64(fails) / float64(len(keep))
		v := verdict{host: host, rate: rate, total: len(keep)}
		for b := range bots {
			v.bots = append(v.bots, b)
		}
		sort.Slice(v.bots, func(i, j int) bool { return v.bots[i] < v.bots[j] })
		switch {
		case rate > ServiceErrorRate && len(keep) >= 3 && !s.down[host]:
			s.down[host] = true
			down = append(down, v)
		case rate <= ServiceErrorRate && s.down[host]:
			delete(s.down, host)
			up = append(up, v)
		}
	}
	s.mu.Unlock()
	for _, v := range down {
		c.Ev.Emit("service_down", 0, events.Error, map[string]any{"host": v.host, "error_rate": v.rate, "bots": v.bots, "connections": v.total})
	}
	for _, v := range up {
		c.Ev.Emit("service_recovered", 0, events.Info, map[string]any{"host": v.host})
	}
}
