// Package counters keeps live per-minute aggregates in the core. Finished
// minutes wait in a ring (up to 6 h) until the panel fetches them and
// confirms the write; only then are they deleted, so a panel restart loses
// nothing.
package counters

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/axiomhq/hyperloglog"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/metrics"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Keep is how long finished minutes wait for the panel.
var Keep = 6 * time.Hour

type agg struct {
	m     metrics.Minute
	users *hyperloglog.Sketch
	chats *hyperloglog.Sketch
}

// Counters is the core's aggregator.
type Counters struct {
	Now func() time.Time

	mu     sync.Mutex
	minute int64 // current minute start
	cur    map[int64]*agg
	ring   []metrics.Minute
	nextID int64
}

// New returns an empty aggregator.
func New() *Counters { return &Counters{Now: time.Now, cur: map[int64]*agg{}, nextID: 1} }

func (c *Counters) bot(id int64) *agg {
	now := c.Now().Unix() / 60 * 60
	if now != c.minute {
		c.rollLocked(now)
	}
	a := c.cur[id]
	if a == nil {
		a = &agg{m: metrics.Minute{BotID: id, TS: now, Counters: map[string]int64{}}}
		c.cur[id] = a
	}
	return a
}

// rollLocked closes the current minute: every bot's aggregate plus their
// sum (bot 0) go to the ring.
func (c *Counters) rollLocked(next int64) {
	if c.minute != 0 && len(c.cur) > 0 {
		sum := metrics.Minute{BotID: 0, TS: c.minute}
		users, chats := hyperloglog.New14(), hyperloglog.New14()
		for _, a := range c.cur {
			m := a.m
			m.Uniq = map[string][]byte{}
			if a.users != nil {
				m.Uniq[metrics.UniqUsers], _ = a.users.MarshalBinary()
				users.Merge(a.users)
			}
			if a.chats != nil {
				m.Uniq[metrics.UniqChats], _ = a.chats.MarshalBinary()
				chats.Merge(a.chats)
			}
			m.ID = c.nextID
			c.nextID++
			c.ring = append(c.ring, m)
			sum.Merge(&a.m)
		}
		sum.Uniq = map[string][]byte{}
		sum.Uniq[metrics.UniqUsers], _ = users.MarshalBinary()
		sum.Uniq[metrics.UniqChats], _ = chats.MarshalBinary()
		sum.ID = c.nextID
		c.nextID++
		c.ring = append(c.ring, sum)
	}
	c.cur = map[int64]*agg{}
	c.minute = next
	// Drop what the panel never fetched within Keep.
	cut := c.Now().Add(-Keep).Unix()
	i := 0
	for i < len(c.ring) && c.ring[i].TS < cut {
		i++
	}
	c.ring = c.ring[i:]
}

// Add increments a counter.
func (c *Counters) Add(botID int64, name string, n int64) {
	c.mu.Lock()
	c.bot(botID).m.Counters[name] += n
	c.mu.Unlock()
}

// Gauge records the maximum of a value over the minute.
func (c *Counters) Gauge(botID int64, name string, v float64) {
	c.mu.Lock()
	a := c.bot(botID)
	if a.m.Gauges == nil {
		a.m.Gauges = map[string]float64{}
	}
	if v > a.m.Gauges[name] {
		a.m.Gauges[name] = v
	}
	c.mu.Unlock()
}

// Latency records a duration into a histogram.
func (c *Counters) Latency(botID int64, name string, d time.Duration) {
	c.mu.Lock()
	a := c.bot(botID)
	if a.m.Hists == nil {
		a.m.Hists = map[string]metrics.Hist{}
	}
	h := a.m.Hists[name]
	if h == nil {
		h = metrics.NewHist()
		a.m.Hists[name] = h
	}
	h.Add(d)
	c.mu.Unlock()
}

func hashID(id int64) []byte {
	b := make([]byte, 8)
	for i := range 8 {
		b[i] = byte(id >> (8 * i))
	}
	return b
}

// Observe is the pipeline's metrics step (step 12).
func (c *Counters) Observe(o ingress.Outcome) {
	u := o.Update
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.bot(u.BotID)
	m := a.m.Counters
	m[metrics.Updates]++
	if u.Type != "" {
		m[metrics.TypePrefix+u.Type]++
	}
	if u.ChatType != "" {
		m[metrics.ChatPrefix+u.ChatType]++
	}
	if u.Language != "" {
		m[metrics.LangPrefix+u.Language]++
	}
	switch {
	case o.Result == "delivered":
		m[metrics.Delivered]++
	case o.Result == "error":
		m[metrics.Errors]++
	case o.Result == "timeout":
		m[metrics.Timeouts]++
	case o.Result == "unavailable":
		m[metrics.Unavailable]++
	case o.Result == "deferred":
		m[metrics.Deferred]++
	case strings.HasPrefix(o.Result, "dropped_"):
		m[metrics.DropPrefix+strings.TrimPrefix(o.Result, "dropped_")]++
	}
	if o.Latency > 0 {
		if a.m.Hists == nil {
			a.m.Hists = map[string]metrics.Hist{}
		}
		h := a.m.Hists[metrics.RespTime]
		if h == nil {
			h = metrics.NewHist()
			a.m.Hists[metrics.RespTime] = h
		}
		h.Add(o.Latency)
	}
	if u.UserID != 0 {
		if a.users == nil {
			a.users = hyperloglog.New14()
		}
		a.users.Insert(hashID(u.UserID))
	}
	if u.ChatID != 0 {
		if a.chats == nil {
			a.chats = hyperloglog.New14()
		}
		a.chats.Insert(hashID(u.ChatID))
	}
}

// NewUser counts a user new to a bot and their source.
func (c *Counters) NewUser(botID, _ int64, source string) {
	c.Add(botID, metrics.NewUsers, 1)
	if source != "" {
		c.Add(botID, metrics.SourcePrefix+source, 1)
	}
}

// Block counts blocks and unblocks.
func (c *Counters) Block(botID, _ int64, blocked bool) {
	if blocked {
		c.Add(botID, metrics.Blocked, 1)
	} else {
		c.Add(botID, metrics.Unblocked, 1)
	}
}

// Payment counts payments by currency (refunds as negative sums).
func (c *Counters) Payment(botID int64, p ingress.Payment) {
	cur := strings.ToUpper(p.Currency)
	if p.Refund {
		c.Add(botID, metrics.PaymentsPrefix+cur+"_refunds", 1)
		c.Add(botID, metrics.PaymentsPrefix+cur+"_sum", -p.Amount)
		return
	}
	c.Add(botID, metrics.PaymentsPrefix+cur+"_count", 1)
	c.Add(botID, metrics.PaymentsPrefix+cur+"_sum", p.Amount)
}

// Since returns finished minutes with id > after, closing the current
// minute first if it is over.
func (c *Counters) Since(after int64, limit int) []metrics.Minute {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now := c.Now().Unix() / 60 * 60; now != c.minute {
		c.rollLocked(now)
	}
	var out []metrics.Minute
	for _, m := range c.ring {
		if m.ID > after {
			out = append(out, m)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

// Ack deletes minutes up to id after the panel wrote them.
func (c *Counters) Ack(upTo int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := 0
	for i < len(c.ring) && c.ring[i].ID <= upTo {
		i++
	}
	c.ring = c.ring[i:]
}

// Pending returns how many finished minutes wait for the panel.
func (c *Counters) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.ring)
}

// Socket methods for the panel.
const (
	MethodMinutes = "metrics.minutes"
	MethodAck     = "metrics.ack"
)

// Register adds the socket methods.
func (c *Counters) Register(mux *wire.Mux) {
	mux.Handle(MethodMinutes, wire.Func(func(_ context.Context, _ *wire.Conn, after int64) ([]metrics.Minute, error) {
		return c.Since(after, 5000), nil
	}))
	mux.Handle(MethodAck, wire.Func(func(_ context.Context, _ *wire.Conn, upTo int64) (struct{}, error) {
		c.Ack(upTo)
		return struct{}{}, nil
	}))
}
