package edge

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Mini Apps: a bot with a subdomain (settings section mini_app) gets
// browser traffic for <sub>.<domain> on the public port. The edge cannot
// reach the bot (another container, loopback only), so it hands the whole
// request to the core, which asks the bot on 127.0.0.1 and returns the
// answer. Everything is bounded: the edge has little memory and serves the
// webhooks of all bots.

// MethodApp is the core method that takes Mini App requests.
const MethodApp = "app.request"

// Bounds of one Mini App exchange.
const (
	MaxAppRequest  = 1 << 20 // body from the browser
	MaxAppResponse = 2 << 20 // body from the bot
	maxAppInflight = 8       // requests the edge holds at once
	appPerMinute   = 600     // requests per IP per minute
	// AppTimeout bounds the bot's answer; the core uses it too.
	AppTimeout = 25 * time.Second
)

// AppRequest is a browser request for a bot's Mini App.
type AppRequest struct {
	BotID  int64               `cbor:"1,keyasint"`
	Method string              `cbor:"2,keyasint"`
	URI    string              `cbor:"3,keyasint"` // path and query
	Header map[string][]string `cbor:"4,keyasint,omitempty"`
	Body   []byte              `cbor:"5,keyasint,omitempty"`
	Host   string              `cbor:"6,keyasint"`
	IP     string              `cbor:"7,keyasint"`
}

// AppResponse is the bot's answer.
type AppResponse struct {
	Status int                 `cbor:"1,keyasint"`
	Header map[string][]string `cbor:"2,keyasint,omitempty"`
	Body   []byte              `cbor:"3,keyasint,omitempty"`
}

// hopHeaders are never passed through (RFC 9110 §7.6.1), nor are the
// forwarding headers a browser could forge.
var hopHeaders = map[string]bool{
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
	"Forwarded": true, "X-Forwarded-For": true, "X-Forwarded-Host": true, "X-Forwarded-Proto": true, "X-Real-Ip": true,
}

// CleanHeader copies h without hop-by-hop and forwarding headers.
func CleanHeader(h http.Header) map[string][]string {
	out := map[string][]string{}
	for k, v := range h {
		k = http.CanonicalHeaderKey(k)
		if hopHeaders[k] || k == "Content-Length" || k == "Host" {
			continue
		}
		out[k] = v
	}
	if c := h.Get("Connection"); c != "" {
		for _, name := range strings.Split(c, ",") {
			delete(out, http.CanonicalHeaderKey(strings.TrimSpace(name)))
		}
	}
	return out
}

// hostName is the request's host without the port, lower case.
func hostName(r *http.Request) string {
	h := r.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// appBot finds the bot whose Mini App lives on host.
func (s *settings) appBot(host string) (int64, BotSettings, bool) {
	if host == "" {
		return 0, BotSettings{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, b := range s.doc.Bots {
		if b.App != "" && b.App == host {
			return id, b, true
		}
	}
	return 0, BotSettings{}, false
}

// certHost lets Let's Encrypt issue certificates only for the panel domain
// and the subdomains bots have: never for any name someone points here.
func (e *Edge) certHost(_ context.Context, host string) error {
	host = strings.ToLower(host)
	if host == e.Cfg.Domain {
		return nil
	}
	if _, _, ok := e.settings.appBot(host); ok {
		return nil
	}
	return errors.New("edge: host is not the panel domain or a bot's Mini App")
}

const appUnavailable = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Недоступно</title><p style="font:16px system-ui;margin:2em">%s</p>`

func appText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	io.WriteString(w, strings.Replace(appUnavailable, "%s", text, 1))
}

// app serves one Mini App request through the core.
func (e *Edge) app(w http.ResponseWriter, r *http.Request, ip netip.Addr, id int64, bs BotSettings) {
	if !e.appLimiter.allow(ip) {
		e.count("app_rate_limited")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost:
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if bs.Maintenance {
		appText(w, http.StatusServiceUnavailable, "Бот на техработах, попробуйте позже.")
		return
	}
	select {
	case e.appSlots <- struct{}{}:
		defer func() { <-e.appSlots }()
	default:
		e.count("app_busy")
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxAppRequest))
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	c := e.Core.Conn()
	if c == nil {
		appText(w, http.StatusServiceUnavailable, "Сервис перезапускается, попробуйте через минуту.")
		return
	}
	req := AppRequest{BotID: id, Method: r.Method, URI: r.URL.RequestURI(), Header: CleanHeader(r.Header),
		Body: body, Host: hostName(r), IP: ip.String()}
	ctx, cancel := context.WithTimeout(r.Context(), AppTimeout+5*time.Second)
	defer cancel()
	var rep AppResponse
	if err := c.Call(ctx, MethodApp, req, &rep); err != nil {
		e.count("app_failed")
		appText(w, http.StatusBadGateway, "Бот сейчас не отвечает, попробуйте через минуту.")
		return
	}
	e.count("app_served")
	h := w.Header()
	for k, v := range rep.Header {
		k = http.CanonicalHeaderKey(k)
		if hopHeaders[k] || k == "Content-Length" {
			continue
		}
		h[k] = v
	}
	// The bot's pages are its own business, but a few defaults cost nothing.
	// No X-Frame-Options: Telegram Web shows Mini Apps in an iframe.
	if h.Get("X-Content-Type-Options") == "" {
		h.Set("X-Content-Type-Options", "nosniff")
	}
	if h.Get("Referrer-Policy") == "" {
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	}
	h.Set("Strict-Transport-Security", "max-age=31536000")
	status := rep.Status
	if status < 100 || status > 999 {
		status = http.StatusBadGateway
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write(rep.Body)
	}
}
