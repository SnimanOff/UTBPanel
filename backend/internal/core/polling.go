package core

import (
	"bytes"
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/edge"
	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
)

// A bot written for getUpdates runs unchanged ("Включить вебхук" in the
// Behaviour tab): Telegram still sends updates to the panel by webhook, and
// the core hands them to the bot through its getUpdates calls. The webhook
// answer waits until the bot confirms the update with the next offset, so
// Telegram's retries, the per-bot gate and the latency metric keep working.
// Until an instance calls getUpdates its updates go to PORT, so a bot
// written for webhooks works the same with the switch on.

// UpdatesPolling is the "updates" setting with the switch on.
const UpdatesPolling = "polling"

// Limits of the getUpdates bridge.
var (
	// PollMaxTimeout caps the long poll; the proxy and libraries expect an
	// answer within a minute.
	PollMaxTimeout = 50 * time.Second
	// PollQueueMax bounds a queue of a bot that stopped calling getUpdates;
	// the oldest updates are dropped beyond it.
	PollQueueMax = 10000
)

type pollItem struct {
	id   int64
	kind string
	body []byte
	sent bool          // returned by getUpdates at least once
	done chan struct{} // closed when confirmed or dropped
}

// pollQueue is the queue of one bot instance.
type pollQueue struct {
	mu      sync.Mutex
	on      bool // the instance was started in polling mode
	polled  bool // getUpdates was called since the start
	items   []*pollItem
	wake    chan struct{} // closed on every change
	waiter  chan struct{} // closed to end the current long poll with 409
	allowed map[string]bool
}

type polls struct {
	mu sync.Mutex
	m  map[string]*pollQueue
}

func pollKey(botID int64, instance string) string {
	if instance == "main" {
		instance = ""
	}
	return strconv.FormatInt(botID, 10) + "/" + instance
}

func (p *polls) queue(botID int64, instance string) *pollQueue {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]*pollQueue{}
	}
	k := pollKey(botID, instance)
	q := p.m[k]
	if q == nil {
		q = &pollQueue{wake: make(chan struct{})}
		p.m[k] = q
	}
	return q
}

func (p *polls) find(botID int64, instance string) *pollQueue {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.m[pollKey(botID, instance)]
}

// changed wakes the long poll; q.mu is held.
func (q *pollQueue) changed() {
	close(q.wake)
	q.wake = make(chan struct{})
}

// drop removes items and releases their webhook answers; q.mu is held.
func (q *pollQueue) drop(keep func(*pollItem) bool) int {
	n, out := 0, q.items[:0]
	for _, it := range q.items {
		if keep(it) {
			out = append(out, it)
			continue
		}
		close(it.done)
		n++
	}
	clear(q.items[len(out):])
	q.items = out
	return n
}

// startPolling is called right before an instance starts.
func (c *Core) startPolling(botID int64, instance string, on bool) {
	q := c.polls.queue(botID, instance)
	q.mu.Lock()
	defer q.mu.Unlock()
	if !on && len(q.items) > 0 {
		n := q.drop(func(*pollItem) bool { return false })
		c.Logs.Add(botID, "core", "updates mode is webhook now: "+strconv.Itoa(n)+" queued getUpdates updates dropped", nowUTC())
	}
	q.on, q.polled = on, false
	if q.waiter != nil {
		close(q.waiter)
		q.waiter = nil
	}
	q.changed()
}

// setPolling turns the mode on or off for a running instance.
func (c *Core) setPolling(botID int64, instance string, on bool) {
	q := c.polls.queue(botID, instance)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.on == on {
		return
	}
	q.on = on
	if !on {
		if n := q.drop(func(*pollItem) bool { return false }); n > 0 {
			c.Logs.Add(botID, "core", "webhook for getUpdates is off: "+strconv.Itoa(n)+" queued updates dropped", nowUTC())
		}
		q.polled = false
		if q.waiter != nil {
			close(q.waiter)
			q.waiter = nil
		}
	}
	q.changed()
}

func (c *Core) registerPolling() {
	// The switch works at once: on, a bot is served on its first getUpdates;
	// off, getUpdates is refused again.
	c.onSettings = append(c.onSettings, func(ctx context.Context, botID int64, key string) {
		if key != "updates" {
			return
		}
		st, err := c.Settings(ctx, botID)
		if err != nil {
			return
		}
		var mode string
		_ = json.Unmarshal(st["updates"], &mode)
		on := mode == UpdatesPolling
		c.setPolling(botID, "", on)
		c.setPolling(botID, "canary", on)
		state := "off"
		if on {
			state = "on"
		}
		c.Logs.Add(botID, "core", "webhook for getUpdates bots is "+state, nowUTC())
	})
}

// polled reports whether an instance in polling mode called getUpdates
// since it started: that is its readiness.
func (c *Core) polled(botID int64, instance string) bool {
	q := c.polls.find(botID, instance)
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.polled
}

// moveCanaryQueue gives updates still waiting for a stopped canary to the
// main instance.
func (c *Core) moveCanaryQueue(botID int64) {
	from := c.polls.find(botID, "canary")
	if from == nil {
		return
	}
	from.mu.Lock()
	items := from.items
	from.items, from.on = nil, false
	from.changed()
	from.mu.Unlock()
	if len(items) == 0 {
		return
	}
	to := c.polls.queue(botID, "")
	to.mu.Lock()
	defer to.mu.Unlock()
	if !to.on {
		for _, it := range items {
			close(it.done)
		}
		return
	}
	for _, it := range items {
		it.sent = false
		to.items = append(to.items, it)
	}
	sort.SliceStable(to.items, func(i, j int) bool { return to.items[i].id < to.items[j].id })
	to.changed()
}

// updateKind is the update type: the first field besides update_id.
func updateKind(body []byte) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	for k := range m {
		if k != "update_id" {
			return k
		}
	}
	return ""
}

// enqueue is the pipeline's Enqueue: an update for an instance in polling
// mode waits in its queue until the bot confirms it.
func (c *Core) enqueue(ctx context.Context, u *ingress.Update, timeout time.Duration) (edge.Reply, string, time.Duration, bool) {
	q := c.polls.find(u.BotID, u.Instance)
	if q == nil {
		return edge.Reply{}, "", 0, false
	}
	var head struct {
		ID int64 `json:"update_id"`
	}
	_ = json.Unmarshal(u.Body, &head)
	it := &pollItem{id: head.ID, kind: updateKind(u.Body), body: u.Body, done: make(chan struct{})}
	q.mu.Lock()
	// Until the bot calls getUpdates it may be a webhook bot: updates go to
	// its PORT as usual, so the mode never hurts a bot that listens there.
	if !q.on || !q.polled {
		q.mu.Unlock()
		return edge.Reply{}, "", 0, false
	}
	if q.allowed != nil && !q.allowed[it.kind] {
		q.mu.Unlock()
		// The bot did not subscribe to this type: Telegram would not send it.
		return edge.Reply{Status: http.StatusOK}, "delivered", 0, true
	}
	if len(q.items) >= PollQueueMax {
		old := q.items[0]
		close(old.done)
		q.items = q.items[1:]
		c.Logs.Add(u.BotID, "core", "getUpdates queue is full: the bot does not call getUpdates, update "+strconv.FormatInt(old.id, 10)+" dropped", nowUTC())
	}
	i := sort.Search(len(q.items), func(i int) bool { return q.items[i].id > it.id })
	q.items = append(q.items, nil)
	copy(q.items[i+1:], q.items[i:])
	q.items[i] = it
	q.changed()
	q.mu.Unlock()

	start := time.Now()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-it.done:
		return edge.Reply{Status: http.StatusOK}, "delivered", time.Since(start), true
	case <-t.C:
		// The bot is slow, not gone: the update stays queued and Telegram
		// gets 200, as with a webhook timeout.
		return edge.Reply{Status: http.StatusOK}, "timeout", time.Since(start), true
	case <-ctx.Done():
		return edge.Reply{Status: http.StatusOK}, "timeout", time.Since(start), true
	}
}

// pollParams are the getUpdates and deleteWebhook parameters a bot sends.
type pollParams struct {
	Offset      int64    `json:"offset"`
	Limit       int      `json:"limit"`
	Timeout     int      `json:"timeout"`
	Allowed     []string `json:"allowed_updates"`
	HasAllowed  bool     `json:"-"`
	DropPending bool     `json:"drop_pending_updates"`
}

// readParams reads Bot API parameters from the query, a form, multipart or
// JSON, like Telegram does.
func readParams(r *http.Request) pollParams {
	var p pollParams
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "application/json" {
		var raw map[string]json.RawMessage
		if json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10)).Decode(&raw) == nil {
			num := func(k string) int64 {
				v := bytes.Trim(raw[k], `"`)
				n, _ := strconv.ParseInt(string(v), 10, 64)
				return n
			}
			p.Offset, p.Limit, p.Timeout = num("offset"), int(num("limit")), int(num("timeout"))
			if v, ok := raw["allowed_updates"]; ok {
				p.HasAllowed = true
				if json.Unmarshal(v, &p.Allowed) != nil {
					var s string
					if json.Unmarshal(v, &s) == nil {
						_ = json.Unmarshal([]byte(s), &p.Allowed)
					}
				}
			}
			p.DropPending = strings.Trim(string(raw["drop_pending_updates"]), `"`) == "true"
		}
		return p
	}
	r.Body = http.MaxBytesReader(nil, r.Body, 64<<10)
	if ct == "multipart/form-data" {
		_ = r.ParseMultipartForm(64 << 10) //nolint:gosec // the body is capped by MaxBytesReader above
	} else {
		_ = r.ParseForm()
	}
	get := func(k string) string { return r.Form.Get(k) }
	p.Offset, _ = strconv.ParseInt(get("offset"), 10, 64)
	p.Limit, _ = strconv.Atoi(get("limit"))
	p.Timeout, _ = strconv.Atoi(get("timeout"))
	if _, ok := r.Form["allowed_updates"]; ok {
		p.HasAllowed = true
		_ = json.Unmarshal([]byte(get("allowed_updates")), &p.Allowed)
	}
	p.DropPending = get("drop_pending_updates") == "true"
	return p
}

func okResult(v any) []byte {
	b, _ := json.Marshal(map[string]any{"ok": true, "result": v})
	return b
}

// getUpdates answers a bot's getUpdates from its queue.
func (c *Core) getUpdates(r *http.Request, botID int64, instance string) ([]byte, int) {
	q := c.polls.find(botID, instance)
	if q == nil {
		return refusedPolling, http.StatusConflict
	}
	p := readParams(r)
	if p.Limit <= 0 || p.Limit > 100 {
		p.Limit = 100
	}
	wait := min(time.Duration(max(p.Timeout, 0))*time.Second, PollMaxTimeout)

	q.mu.Lock()
	if !q.on {
		q.mu.Unlock()
		return refusedPolling, http.StatusConflict
	}
	if !q.polled {
		q.polled = true
		c.Logs.Add(botID, "core", "bot called getUpdates: updates come from the panel's webhook", nowUTC())
	}
	// A newer request ends the waiting one, as at Telegram.
	if q.waiter != nil {
		close(q.waiter)
	}
	mine := make(chan struct{})
	q.waiter = mine
	if p.HasAllowed {
		q.allowed = nil
		if len(p.Allowed) > 0 {
			q.allowed = map[string]bool{}
			for _, k := range p.Allowed {
				q.allowed[k] = true
			}
		}
	}
	// Confirm: everything returned before with a lower id. A negative
	// offset keeps only the last -offset updates.
	if p.Offset < 0 {
		keepFrom := len(q.items) + int(p.Offset)
		idx := 0
		q.drop(func(*pollItem) bool { idx++; return idx > keepFrom })
	} else if p.Offset > 0 {
		q.drop(func(it *pollItem) bool { return !it.sent || it.id >= p.Offset })
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		out := make([]json.RawMessage, 0, min(len(q.items), p.Limit))
		for _, it := range q.items {
			if len(out) == p.Limit {
				break
			}
			if q.allowed != nil && !q.allowed[it.kind] {
				continue
			}
			it.sent = true
			out = append(out, it.body)
		}
		if len(out) > 0 || wait == 0 {
			if q.waiter == mine {
				q.waiter = nil
			}
			q.mu.Unlock()
			return okResult(out), http.StatusOK
		}
		wake := q.wake
		q.mu.Unlock()
		select {
		case <-wake:
			q.mu.Lock()
		case <-mine:
			return []byte(`{"ok":false,"error_code":409,"description":"Conflict: terminated by other getUpdates request; make sure that only one bot instance is running"}`), http.StatusConflict
		case <-deadline.C:
			q.mu.Lock()
			wait = 0
		case <-r.Context().Done():
			q.mu.Lock()
			if q.waiter == mine {
				q.waiter = nil
			}
			q.mu.Unlock()
			return okResult([]json.RawMessage{}), http.StatusOK
		}
		if q.waiter != mine {
			q.mu.Unlock()
			return []byte(`{"ok":false,"error_code":409,"description":"Conflict: terminated by other getUpdates request; make sure that only one bot instance is running"}`), http.StatusConflict
		}
	}
}

// deleteWebhookPolling handles deleteWebhook of a polling bot: libraries
// call it before polling, drop_pending_updates empties the queue.
func (c *Core) deleteWebhookPolling(r *http.Request, botID int64, instance string) {
	q := c.polls.find(botID, instance)
	if q == nil {
		return
	}
	p := readParams(r)
	if !p.DropPending {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.on {
		return
	}
	if n := q.drop(func(*pollItem) bool { return false }); n > 0 {
		c.Logs.Add(botID, "core", "deleteWebhook(drop_pending_updates): "+strconv.Itoa(n)+" queued updates dropped", nowUTC())
	}
}

// webhookInfoPolling shows a polling bot no webhook, otherwise libraries
// refuse to poll; pending is the queue length.
func (c *Core) webhookInfoPolling(botID int64, instance string) ([]byte, bool) {
	q := c.polls.find(botID, instance)
	if q == nil {
		return nil, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.on {
		return nil, false
	}
	return okResult(map[string]any{"url": "", "has_custom_certificate": false, "pending_update_count": len(q.items)}), true
}

// pollingOn reports whether an instance runs in polling mode.
func (c *Core) pollingOn(botID int64, instance string) bool {
	q := c.polls.find(botID, instance)
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.on
}

var refusedPolling = []byte(`{"ok":false,"error_code":409,"description":"Conflict: tgpanel: бот работает через вебхук; для бота под polling включите «Включить вебхук» во вкладке «Поведение»"}`)
