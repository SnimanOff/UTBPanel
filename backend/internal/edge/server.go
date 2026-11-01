package edge

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Update is a webhook handed to the core.
type Update struct {
	BotID    int64  `cbor:"1,keyasint"`
	Body     []byte `cbor:"2,keyasint"`
	Received int64  `cbor:"3,keyasint"` // unix nanoseconds
}

// Reply is what goes back to Telegram in the webhook response.
type Reply struct {
	Status      int    `cbor:"1,keyasint"`
	ContentType string `cbor:"2,keyasint,omitempty"`
	Body        []byte `cbor:"3,keyasint,omitempty"`
}

// MethodUpdate is the core method that takes webhooks.
const MethodUpdate = "ingress.update"

// coreTimeout: after it the core counts as unavailable for this update.
var coreTimeout = 2 * time.Second

// handler routes the public port: POST /hook/<bot_id> → core,
// POST /gh/<bot_id> → core (GitHub releases), everything else 404 without a body. There is
// no UI or API on this port.
func (e *Edge) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		switch {
		case strings.HasPrefix(r.URL.Path, "/hook/") && r.Method == http.MethodPost:
			e.hook(w, r, ip)
		case strings.HasPrefix(r.URL.Path, "/gh/") && r.Method == http.MethodPost:
			e.githubHook(w, r, ip)
		default:
			if !e.limiter.allow(ip) {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func clientIP(r *http.Request) netip.Addr {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	a, _ := netip.ParseAddr(host)
	return a.Unmap()
}

func (e *Edge) fromTelegram(ip netip.Addr) bool {
	for _, p := range e.telegram {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func botFromPath(path, prefix string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimPrefix(path, prefix), 10, 64)
	return id, err == nil && id > 0
}

func (e *Edge) hook(w http.ResponseWriter, r *http.Request, ip netip.Addr) {
	// Foreign requests are cut before the secret is even looked at.
	if !e.fromTelegram(ip) {
		e.count("rejected_ip")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	id, ok := botFromPath(r.URL.Path, "/hook/")
	secret, known := e.settings.secret(id)
	if !ok || !known {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	if subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
		e.count("rejected_secret")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(e.Cfg.MaxBody)))
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	u := Update{BotID: id, Body: body, Received: time.Now().UnixNano()}
	rep, err := e.toCore(r.Context(), u)
	if err != nil {
		rep = e.withoutCore(u)
	}
	writeReply(w, rep)
}

func writeReply(w http.ResponseWriter, rep Reply) {
	if rep.Status == 0 {
		rep.Status = http.StatusOK
	}
	if len(rep.Body) > 0 {
		ct := rep.ContentType
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(rep.Status)
	w.Write(rep.Body)
}

// errNoCore means the core did not take the update.
var errNoCore = errors.New("edge: core unavailable")

// toCore hands the update to the core. The core waits for the bot itself,
// so only the hand-over is bounded here; when the core is silent for
// coreTimeout the edge answers by itself.
func (e *Edge) toCore(ctx context.Context, u Update) (Reply, error) {
	c := e.Core.Conn()
	if c == nil {
		return Reply{}, errNoCore
	}
	ctx, cancel := context.WithTimeout(ctx, e.botTimeout())
	defer cancel()
	type result struct {
		r   Reply
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var rep Reply
		err := c.Call(ctx, MethodUpdate, u, &rep)
		ch <- result{rep, err}
	}()
	// The core answers within coreTimeout, or the edge checks that it is
	// alive at all. A slow bot is the core's business: while the core lives
	// the edge keeps waiting for its answer.
	select {
	case res := <-ch:
		if res.err != nil {
			return Reply{}, errNoCore
		}
		return res.r, nil
	case <-time.After(coreTimeout):
		if !e.coreAlive(c) {
			return Reply{}, errNoCore
		}
	}
	res := <-ch
	if res.err != nil {
		return Reply{}, errNoCore
	}
	return res.r, nil
}

// coreAlive pings the core quickly.
func (e *Edge) coreAlive(c interface {
	Call(context.Context, string, any, any) error
}) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	return c.Call(ctx, "ping", nil, nil) == nil
}

func (e *Edge) botTimeout() time.Duration { return 60 * time.Second }

// limiter bounds requests to unknown paths per IP (scanners).
type limiter struct {
	mu    sync.Mutex
	hits  map[netip.Addr]int
	reset time.Time
	max   int
}

func (l *limiter) allow(ip netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Now().After(l.reset) {
		l.hits = map[netip.Addr]int{}
		l.reset = time.Now().Add(time.Minute)
	}
	l.hits[ip]++
	return l.hits[ip] <= l.max
}
