package api

import (
	"mime"
	"net"
	"net/http"
	"strings"
)

// Guard protects the API behind the gate's local port: any site
// open in the browser and any program on the computer can send requests to
// 127.0.0.1.
//
//   - Host must be 127.0.0.1:<port> or localhost:<port> (DNS rebinding);
//   - changing requests need application/json (uploads: multipart),
//     X-Panel: 1, and an Origin that is absent or the gate itself;
//   - no CORS headers are ever sent, so foreign sites fail the preflight.
//
// Requests of gate.exe itself to /enroll carry the server address in Host
// and are exempt: an enrollment certificate is good for nothing else.
func Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/enroll" || strings.HasPrefix(r.URL.Path, "/enroll/") {
			next.ServeHTTP(w, r)
			return
		}
		if !localHost(r.Host) {
			WriteError(w, http.StatusForbidden, "bad_host", "requests must come through gate (Host 127.0.0.1 or localhost)")
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			if r.Method == http.MethodOptions {
				// No CORS: a preflight never succeeds.
				w.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-Panel") != "1" {
			WriteError(w, http.StatusForbidden, "csrf", "missing X-Panel: 1 header")
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !localOrigin(o) {
			WriteError(w, http.StatusForbidden, "csrf", "foreign Origin")
			return
		}
		if r.ContentLength != 0 || r.Header.Get("Transfer-Encoding") != "" {
			mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if mt != "application/json" && mt != "multipart/form-data" {
				WriteError(w, http.StatusUnsupportedMediaType, "csrf", "body must be application/json or multipart/form-data")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func localHost(host string) bool {
	h, port, err := net.SplitHostPort(host)
	if err != nil || port == "" {
		return false
	}
	return h == "127.0.0.1" || h == "localhost"
}

func localOrigin(o string) bool {
	rest, ok := strings.CutPrefix(o, "http://")
	return ok && localHost(rest)
}
