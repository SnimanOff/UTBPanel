package edge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
