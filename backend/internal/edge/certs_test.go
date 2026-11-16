package edge

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/acme"

	"github.com/SnimanOff/UTBPanel/backend/internal/config"
)

func quietEdge(t *testing.T) *Edge {
	t.Helper()
	cfg := config.DefaultEdge()
	cfg.DataDir, cfg.RunDir = t.TempDir(), t.TempDir()
	cfg.PublicAddr = "203.0.113.10"
	e, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// A name without a Let's Encrypt certificate must still bring up a page: the
// handshake falls back to the self-signed certificate instead of failing.
func TestLogCertErrorsFallsBack(t *testing.T) {
	e := quietEdge(t)
	boom := errors.New("no certificate")
	base := func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return nil, boom }
	get := e.logCertErrors(base)

	if cert, err := get(&tls.ClientHelloInfo{ServerName: "typo.example.test"}); cert != nil || err != nil {
		t.Fatalf("want the fallback, got %v, %v", cert, err)
	}
	// The tls-alpn-01 token request keeps its error: a fallback certificate
	// would fail the challenge with a misleading message.
	if _, err := get(&tls.ClientHelloInfo{ServerName: "x.example.test", SupportedProtos: []string{acme.ALPNProto}}); !errors.Is(err, boom) {
		t.Fatalf("token request: %v", err)
	}
}

func TestUnknownHostPage(t *testing.T) {
	e := quietEdge(t)
	e.Cfg.Domain = "example.test"

	r := httptest.NewRequest("GET", "https://typo.example.test/", nil)
	r.Host = "typo.example.test"
	w := httptest.NewRecorder()
	e.handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "typo.example.test") || !strings.Contains(body, "example.test") {
		t.Fatalf("the page must name the host and the domain: %s", body)
	}

	// A configured page is served as before.
	if err := e.settings.apply(Settings{Pages: map[string][]byte{"page.example.test": []byte("hello")}}); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", "https://page.example.test/", nil)
	r.Host = "page.example.test"
	w = httptest.NewRecorder()
	e.handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "hello") {
		t.Fatalf("page: %d %s", w.Code, w.Body.String())
	}
}

// The domain from the environment may differ in case from the request: the
// host policy must not depend on it.
func TestCertHostIgnoresCase(t *testing.T) {
	e := quietEdge(t)
	e.Cfg.Domain = "Example.Test"
	if err := e.settings.apply(Settings{Pages: map[string][]byte{"page.example.test": []byte("x")}}); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"example.test", "EXAMPLE.TEST", "page.example.test", "PAGE.example.test"} {
		if err := e.certHost(context.Background(), host); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
	if err := e.certHost(context.Background(), "other.test"); err == nil {
		t.Error("a foreign host was allowed")
	}
}

func TestHostSet(t *testing.T) {
	e := quietEdge(t)
	doc := Settings{
		Pages: map[string][]byte{"page.example.test": []byte("x")},
		Bots:  map[int64]BotSettings{7: {Secret: "s", App: "bot.example.test"}},
	}
	if err := e.settings.apply(doc); err != nil {
		t.Fatal(err)
	}
	got := e.settings.hostSet()
	for _, host := range []string{"page.example.test", "bot.example.test"} {
		if !got[host] {
			t.Errorf("%s is missing from %v", host, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("hosts = %v", got)
	}
}
