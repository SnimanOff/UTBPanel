package edge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"net/http"
	"net/netip"
	"time"
)

// Static pages: one HTML file per subdomain, pushed by the core in
// Settings.Pages and served from memory, so they work while the core is
// down. Only "/" (and "/index.html") exists; the page brings its CSS and
// JS inline.

// page returns the HTML of the page on host.
func (s *settings) page(host string) ([]byte, bool) {
	if host == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.doc.Pages[host]
	return b, ok
}

// unknownHost answers a name that is neither the panel domain nor a
// configured page or Mini App: a 404 with the reason, so a typo in a
// subdomain does not look like a broken certificate.
func (e *Edge) unknownHost(w http.ResponseWriter, host string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNotFound)
	// html/template escapes the names: the host comes from the request.
	_ = unknownHostPage.Execute(w, struct{ Host, Domain string }{host, e.Cfg.Domain})
}

// unknownHostPage is static except for the two names it is given.
var unknownHostPage = template.Must(template.New("unknown").Parse(`<!doctype html><meta charset="utf-8">` +
	`<meta name="viewport" content="width=device-width">` +
	`<title>404 · адрес не настроен</title>` +
	`<style>body{font:16px system-ui;margin:2em;max-width:42em;line-height:1.5}` +
	`code{font-family:ui-monospace,monospace;background:#f3f3f3;padding:1px 4px}</style>` +
	`<h1>404</h1><p>На этом адресе ничего нет: <code>{{.Host}}</code> — не домен панели, ` +
	`не страница и не Mini App бота.</p>` +
	`<p>Страницы и Mini App открываются на поддоменах вида <code>&lt;label&gt;.{{.Domain}}</code>, ` +
	`например <code>test.{{.Domain}}</code>.</p>`))

// servePage answers one request for a static page.
func (e *Edge) servePage(w http.ResponseWriter, r *http.Request, ip netip.Addr, html []byte) {
	if !e.appLimiter.allow(ip) {
		e.count("page_rate_limited")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	h := w.Header()
	h.Set("Strict-Transport-Security", "max-age=31536000")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	e.count("page_served")
	sum := sha256.Sum256(html)
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache") // revalidate: a new version shows at once
	h.Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(html))
}
