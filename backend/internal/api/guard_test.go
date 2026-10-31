package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	h := Guard(ok)
	type tc struct {
		name, method, path, host, origin, ctype, xpanel, body string
		want                                                 int
	}
	cases := []tc{
		{"get through gate", "GET", "/api/bots", "127.0.0.1:47100", "", "", "", "", 204},
		{"get via localhost", "GET", "/api/bots", "localhost:47100", "", "", "", "", 204},
		{"dns rebinding", "GET", "/api/bots", "evil.example:47100", "", "", "", "", 403},
		{"no port", "GET", "/api/bots", "127.0.0.1", "", "", "", "", 403},
		{"post ok", "POST", "/api/bots", "127.0.0.1:47100", "http://127.0.0.1:47100", "application/json", "1", "{}", 204},
		{"post without origin", "POST", "/api/bots", "127.0.0.1:47100", "", "application/json", "1", "{}", 204},
		{"post without x-panel", "POST", "/api/bots", "127.0.0.1:47100", "", "application/json", "", "{}", 403},
		{"foreign origin", "POST", "/api/bots", "127.0.0.1:47100", "https://evil.example", "application/json", "1", "{}", 403},
		{"null origin", "POST", "/api/bots", "127.0.0.1:47100", "null", "application/json", "1", "{}", 403},
		{"form post", "POST", "/api/bots", "127.0.0.1:47100", "", "application/x-www-form-urlencoded", "1", "a=b", 415},
		{"text post", "POST", "/api/bots", "127.0.0.1:47100", "", "text/plain", "1", "{}", 415},
		{"upload", "POST", "/api/bots/1/releases", "127.0.0.1:47100", "", "multipart/form-data; boundary=x", "1", "--x--", 204},
		{"empty post", "POST", "/api/bots/1/start", "127.0.0.1:47100", "", "", "1", "", 204},
		{"preflight", "OPTIONS", "/api/bots", "127.0.0.1:47100", "https://evil.example", "", "", "", 403},
		{"enroll from gate", "POST", "/enroll", "203.0.113.10:443", "", "application/json", "1", "{}", 204},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "http://x"+c.path, strings.NewReader(c.body))
			r.Host = c.host
			if c.body == "" {
				r.ContentLength = 0
			}
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if c.ctype != "" {
				r.Header.Set("Content-Type", c.ctype)
			}
			if c.xpanel != "" {
				r.Header.Set("X-Panel", c.xpanel)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d: %s", w.Code, c.want, w.Body)
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("CORS header sent")
			}
		})
	}
}
