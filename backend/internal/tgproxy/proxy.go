// Package tgproxy is the transparent interception of a bot's outgoing
// calls: the bot gets HTTPS_PROXY pointing here, which the standard Go HTTP
// client honours by itself. Connections to api.telegram.org are decrypted
// with the interception CA, counted and passed on to Telegram; every other
// address is a plain tunnel — the core only sees where the bot connects.
package tgproxy

import (
	"bufio"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Call is one Bot API call seen by the proxy.
type Call struct {
	BotID      int64
	Instance   string // main | canary
	Method     string
	Status     int // HTTP status from Telegram
	ErrorCode  int // Telegram error_code, 0 on success
	RetryAfter int
	Latency    time.Duration
	Local      bool // answered by the proxy itself (setWebhook…)
}

// Conn is one connection to another service.
type Conn struct {
	BotID    int64
	Instance string
	Host     string // host:port
	Err      string // dns | refused | timeout | other; empty on success
	Connect  time.Duration
	BytesIn  int64
	BytesOut int64
	Done     bool // the connection closed (bytes are final)
}

// Hooks receive what the proxy sees.
type Hooks struct {
	OnCall func(Call)
	OnConn func(Conn)
	// Intercept answers a Bot API method locally; ok=false passes it on.
	Intercept func(botID int64, method string) (body []byte, status int, ok bool)
	// Line writes a [core] line into the bot's log.
	Line func(botID int64, text string)
}

// Proxy serves all bot instances; each has its own port, so the bot and the
// instance are known without guessing.
type Proxy struct {
	CA       *CA
	Upstream string // https://api.telegram.org (tests use a fake)
	Hooks    Hooks
	Log      *slog.Logger
	// UpstreamTLS trusts the fake Telegram in tests.
	UpstreamTLS *tls.Config
	// UID returns the uid a bot runs under; when set, a connection to a
	// bot's port from another uid is closed. Ports are on 127.0.0.1, which
	// every bot can reach: without this one bot could make calls on behalf
	// of another and get its interceptions and metrics.
	UID func(botID int64) int

	client *http.Client
	mu     sync.Mutex
	lns    map[string]net.Listener // botID/instance → listener
}

func (p *Proxy) httpClient() *http.Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == nil {
		tr := &http.Transport{
			Proxy:               nil,
			TLSClientConfig:     p.UpstreamTLS,
			MaxIdleConnsPerHost: 32,
			IdleConnTimeout:     90 * time.Second,
			ForceAttemptHTTP2:   true,
		}
		p.client = &http.Client{Transport: tr}
	}
	return p.client
}

func key(botID int64, inst string) string { return strconv.FormatInt(botID, 10) + "/" + inst }

// Start listens for one bot instance on 127.0.0.1:port.
func (p *Proxy) Start(botID int64, instance string, port int) error {
	k := key(botID, instance)
	p.mu.Lock()
	if p.lns == nil {
		p.lns = map[string]net.Listener{}
	}
	if _, ok := p.lns[k]; ok {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	p.serve(botID, instance, ln)
	return nil
}

// serve answers on a bot instance's port, only to that bot when UID is set.
func (p *Proxy) serve(botID int64, instance string, ln net.Listener) {
	if p.UID != nil {
		ln = &ownerListener{Listener: ln, uid: p.UID(botID), bot: botID, log: p.Log}
	}
	p.mu.Lock()
	if p.lns == nil {
		p.lns = map[string]net.Listener{}
	}
	p.lns[key(botID, instance)] = ln
	p.mu.Unlock()
	srv := &http.Server{Handler: &handler{p: p, bot: botID, inst: instance}, ReadHeaderTimeout: 30 * time.Second}
	go srv.Serve(ln)
}

// Stop closes the listener of an instance.
func (p *Proxy) Stop(botID int64, instance string) {
	p.mu.Lock()
	ln := p.lns[key(botID, instance)]
	delete(p.lns, key(botID, instance))
	p.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
}

// ownerListener accepts only connections of one uid.
type ownerListener struct {
	net.Listener
	uid int
	bot int64
	log *slog.Logger
}

func (l *ownerListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if uid, ok := peerUID(c); ok && uid == l.uid {
			return c, nil
		}
		if l.log != nil {
			l.log.Warn("tgproxy: connection from another uid refused", "bot", l.bot)
		}
		c.Close()
	}
}

type handler struct {
	p    *Proxy
	bot  int64
	inst string
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		h.connect(w, r)
		return
	}
	// Plain http:// requests through HTTP_PROXY: passed on as they are.
	if r.URL.Host == "" {
		http.Error(w, "tgproxy: absolute URL required", http.StatusBadRequest)
		return
	}
	start := time.Now()
	out := r.Clone(r.Context())
	out.RequestURI = ""
	resp, err := http.DefaultTransport.RoundTrip(out)
	c := Conn{BotID: h.bot, Instance: h.inst, Host: hostPort(r.URL.Host, "80"), Connect: time.Since(start), Done: true}
	if err != nil {
		c.Err = classify(err)
		h.p.conn(c)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(w, resp.Body)
	c.BytesIn, c.BytesOut = n, r.ContentLength
	h.p.conn(c)
}

func hostPort(h, port string) string {
	if _, _, err := net.SplitHostPort(h); err == nil {
		return h
	}
	return net.JoinHostPort(h, port)
}

func (p *Proxy) conn(c Conn) {
	if p.Hooks.OnConn != nil {
		p.Hooks.OnConn(c)
	}
}

func classify(err error) string {
	var dnsErr *net.DNSError
	var ne net.Error
	switch {
	case errors.As(err, &dnsErr):
		return "dns"
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case strings.Contains(err.Error(), "refused"):
		return "refused"
	}
	return "other"
}

func (h *handler) connect(w http.ResponseWriter, r *http.Request) {
	target := hostPort(r.Host, "443")
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	if host, _, _ := net.SplitHostPort(target); host == TelegramHost {
		client, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		h.telegram(&bufConn{Conn: client, r: buf.Reader})
		return
	}
	start := time.Now()
	up, err := net.DialTimeout("tcp", target, 10*time.Second)
	c := Conn{BotID: h.bot, Instance: h.inst, Host: target, Connect: time.Since(start)}
	if err != nil {
		c.Err, c.Done = classify(err), true
		h.p.conn(c)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	h.p.conn(c)
	client, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	// A simple tunnel: nothing is decrypted, only bytes are counted.
	var in, out int64
	done := make(chan struct{})
	go func() {
		n, _ := io.Copy(up, buf.Reader)
		out = n
		up.(*net.TCPConn).CloseWrite()
		close(done)
	}()
	in, _ = io.Copy(client, up)
	<-done
	client.Close()
	up.Close()
	c.BytesIn, c.BytesOut, c.Done = in, out, true
	h.p.conn(c)
}

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// oneConn is a listener that yields a single connection.
type oneConn struct {
	c    net.Conn
	once sync.Once
	done chan struct{}
}

func (o *oneConn) Accept() (net.Conn, error) {
	var c net.Conn
	o.once.Do(func() { c = o.c })
	if c != nil {
		return &closeNotify{Conn: c, done: o.done}, nil
	}
	<-o.done
	return nil, net.ErrClosed
}
func (o *oneConn) Close() error   { return nil }
func (o *oneConn) Addr() net.Addr { return o.c.LocalAddr() }

type closeNotify struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *closeNotify) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}

// telegram serves decrypted requests to api.telegram.org on one connection.
func (h *handler) telegram(c net.Conn) {
	tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{*h.p.CA.leaf}, NextProtos: []string{"http/1.1"}})
	srv := &http.Server{Handler: http.HandlerFunc(h.forward), ReadHeaderTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second}
	srv.Serve(&oneConn{c: tc, done: make(chan struct{})})
}

// method extracts the Bot API method from /bot<token>/<method>.
func method(path string) string {
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)
	if len(parts) >= 2 && strings.HasPrefix(parts[0], "bot") {
		return parts[1]
	}
	if len(parts) >= 1 && parts[0] == "file" {
		return "file"
	}
	return ""
}

func (h *handler) forward(w http.ResponseWriter, r *http.Request) {
	m := method(r.URL.Path)
	if h.p.Hooks.Intercept != nil && m != "" {
		if body, status, ok := h.p.Hooks.Intercept(h.bot, m); ok {
			io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			w.Write(body)
			if h.p.Hooks.OnCall != nil {
				h.p.Hooks.OnCall(Call{BotID: h.bot, Instance: h.inst, Method: m, Status: status, Local: true})
			}
			return
		}
	}
	base := h.p.Upstream
	if base == "" {
		base = "https://" + TelegramHost
	}
	u, _ := url.Parse(base)
	out, err := http.NewRequestWithContext(r.Context(), r.Method, u.Scheme+"://"+u.Host+r.URL.RequestURI(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out.Header = r.Header.Clone()
	out.ContentLength = r.ContentLength
	start := time.Now()
	resp, err := h.p.httpClient().Do(out)
	call := Call{BotID: h.bot, Instance: h.inst, Method: m, Latency: time.Since(start)}
	if err != nil {
		call.Status = http.StatusBadGateway
		h.report(call)
		http.Error(w, "tgproxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	call.Status = resp.StatusCode
	for k, vs := range resp.Header {
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode >= 400 && m != "file" {
		// Errors are small JSON: read them to count codes and retry_after.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		call.ErrorCode, call.RetryAfter = parseError(body)
		w.Write(body)
	} else {
		io.Copy(w, resp.Body)
	}
	h.report(call)
}

func (h *handler) report(c Call) {
	if h.p.Hooks.OnCall != nil {
		h.p.Hooks.OnCall(c)
	}
}

func parseError(b []byte) (code, retry int) {
	// {"ok":false,"error_code":429,"description":"…","parameters":{"retry_after":7}}
	s := string(b)
	if i := strings.Index(s, `"error_code":`); i >= 0 {
		code, _ = strconv.Atoi(strings.TrimSpace(strings.SplitN(strings.SplitN(s[i+13:], ",", 2)[0], "}", 2)[0]))
	}
	if i := strings.Index(s, `"retry_after":`); i >= 0 {
		retry, _ = strconv.Atoi(strings.TrimSpace(strings.SplitN(strings.SplitN(s[i+14:], ",", 2)[0], "}", 2)[0]))
	}
	return
}

// Env returns the variables that point a bot at its proxy.
func Env(port int, bundle string) map[string]string {
	addr := "http://127.0.0.1:" + strconv.Itoa(port)
	return map[string]string{"HTTPS_PROXY": addr, "HTTP_PROXY": addr, "https_proxy": addr, "http_proxy": addr, "SSL_CERT_FILE": bundle}
}
