package core

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/tgproxy"
)

// BypassAfter: updates were delivered, yet no Bot API call came through the
// proxy and no method was answered in the webhook body for this long.
var BypassAfter = 10 * time.Minute

// apiWatch remembers the last Bot API activity and delivery of every bot.
type apiWatch struct {
	mu        sync.Mutex
	api       map[int64]time.Time
	delivered map[int64]time.Time
	warned    map[int64]time.Time
}

func (w *apiWatch) touch(m map[int64]time.Time, id int64) {
	w.mu.Lock()
	m[id] = time.Now()
	w.mu.Unlock()
}

// onCall counts a Bot API call seen by the proxy.
func (c *Core) onCall(call tgproxy.Call) {
	id := call.BotID
	c.Count.Add(id, "api_calls", 1)
	if call.Method != "" {
		c.Count.Add(id, "api_m_"+call.Method, 1)
	}
	if call.ErrorCode != 0 {
		c.Count.Add(id, "api_err_"+strconv.Itoa(call.ErrorCode), 1)
		if call.ErrorCode == 429 {
			c.Ev.Emit("telegram_429", id, events.Warn, map[string]any{"method": call.Method, "retry_after": call.RetryAfter})
		}
	} else if call.Status >= 500 {
		c.Count.Add(id, "api_err_5xx", 1)
	}
	if call.Latency > 0 {
		c.Count.Latency(id, "api", call.Latency)
	}
	c.Live.APICall(id)
	c.apiW.touch(c.apiW.api, id)
}

// observeDelivery feeds the bypass check from the pipeline.
func (c *Core) observeDelivery(o ingress.Outcome) {
	if o.Result != "delivered" {
		return
	}
	c.apiW.touch(c.apiW.delivered, o.Update.BotID)
	if o.ReplyMethod {
		c.apiW.touch(c.apiW.api, o.Update.BotID)
	}
}

// checkBypass warns once a day about bots whose library ignores
// HTTPS_PROXY: their Bot API metrics would silently be missing.
func (c *Core) checkBypass(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		for _, id := range c.runningBots() {
			st := c.Runner.Status(id)
			if now.Sub(st.Started) < BypassAfter {
				continue
			}
			c.apiW.mu.Lock()
			del, api, warned := c.apiW.delivered[id], c.apiW.api[id], c.apiW.warned[id]
			bypass := now.Sub(del) < BypassAfter && now.Sub(api) >= BypassAfter && now.Sub(warned) > 24*time.Hour
			if bypass {
				c.apiW.warned[id] = now
			}
			c.apiW.mu.Unlock()
			if bypass {
				c.Ev.Emit("bot_bypasses_proxy", id, events.Warn, map[string]any{
					"hint": "бот получает апдейты, но его вызовы Bot API не идут через прокси: метрик Bot API не будет; используйте стандартный http.Client",
				})
			}
		}
	}
}
