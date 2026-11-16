package edge

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

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

func TestACMECachesAreIsolated(t *testing.T) {
	e := quietEdge(t)
	e.Cfg.Domain = "example.test"
	e.Cfg.PublicAddr = e.Cfg.Domain

	_, _, production, err := e.tlsSetup()
	if err != nil {
		t.Fatal(err)
	}
	e.Cfg.ACMEStaging = true
	_, _, staging, err := e.tlsSetup()
	if err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	key := e.Cfg.Domain
	if err := staging.Cache.Put(ctx, key, []byte("staging entry")); err != nil {
		t.Fatal(err)
	}
	if _, err := production.Cache.Get(ctx, key); !errors.Is(err, autocert.ErrCacheMiss) {
		t.Fatalf("production cache lookup: got %v, want cache miss", err)
	}
	if err := production.Cache.Put(ctx, key, []byte("production entry")); err != nil {
		t.Fatal(err)
	}
	if data, err := staging.Cache.Get(ctx, key); err != nil || string(data) != "staging entry" {
		t.Fatalf("staging cache lookup: got %q, %v", data, err)
	}
}

func TestFallbackPreservesTelegramCertificate(t *testing.T) {
	e := quietEdge(t)
	ip := e.Cfg.PublicAddr
	_, originalPEM, _, err := e.tlsSetup()
	if err != nil {
		t.Fatal(err)
	}

	e.Cfg.Domain = "example.test"
	e.Cfg.PublicAddr = e.Cfg.Domain
	_, domainPEM, _, err := e.tlsSetup()
	if err != nil {
		t.Fatal(err)
	}
	if len(domainPEM) != 0 {
		t.Fatal("domain mode must not pass its fallback certificate to Telegram")
	}

	e.Cfg.Domain = ""
	e.Cfg.PublicAddr = ip
	_, restoredPEM, _, err := e.tlsSetup()
	if err != nil {
		t.Fatal(err)
	}
	if len(originalPEM) == 0 || !bytes.Equal(originalPEM, restoredPEM) {
		t.Fatal("the Telegram certificate changed after switching back to IP mode")
	}
}

type cacheWithoutIssuance struct{ autocert.Cache }

func (c cacheWithoutIssuance) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := c.Cache.Get(ctx, key)
	if errors.Is(err, autocert.ErrCacheMiss) {
		return nil, errors.New("test cache miss: ACME issuance disabled")
	}
	return data, err
}

func TestWarmCertificateServesECDSAClient(t *testing.T) {
	e := quietEdge(t)
	e.Cfg.Domain = "example.test"
	e.Cfg.PublicAddr = e.Cfg.Domain
	host := "page.example.test"
	if err := e.settings.apply(Settings{Pages: map[string][]byte{host: []byte("hello")}}); err != nil {
		t.Fatal(err)
	}

	tlsCfg, _, manager, err := e.tlsSetup()
	if err != nil {
		t.Fatal(err)
	}
	fixtureDir := t.TempDir()
	_, certPEM, err := selfSigned(fixtureDir, host)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(filepath.Join(fixtureDir, "selfsigned.key"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := manager.Cache.Put(ctx, host, append(keyPEM, certPEM...)); err != nil {
		t.Fatal(err)
	}
	manager.Cache = cacheWithoutIssuance{manager.Cache}
	previous := acmeManager.Swap(manager)
	t.Cleanup(func() { acmeManager.Store(previous) })
	e.warmCert(ctx, host)
	// Require the handshake to use the certificate loaded into memory by warm-up.
	if err := manager.Cache.Delete(ctx, host); err != nil {
		t.Fatal(err)
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("could not load the fixture certificate")
	}
	serverConn, clientConn := net.Pipe()
	defer func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	}()
	handshakeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	serverResult := make(chan error, 1)
	go func() { serverResult <- tls.Server(serverConn, tlsCfg).HandshakeContext(handshakeCtx) }()
	client := tls.Client(clientConn, &tls.Config{ServerName: host, RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err := client.HandshakeContext(handshakeCtx); err != nil {
		t.Fatal(err)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestCertWarningsGroupUnknownNames(t *testing.T) {
	e := quietEdge(t)
	e.Cfg.Domain = "example.test"
	page := "page.example.test"
	if err := e.settings.apply(Settings{Pages: map[string][]byte{page: []byte("hello")}}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	e.Log = slog.New(slog.NewTextHandler(&logs, nil))
	err := errors.New("no certificate")
	for _, name := range []string{"unknown.example.test", "other.example.test", e.Cfg.Domain, "EXAMPLE.TEST", page} {
		e.warnCert(name, err)
	}
	if got := strings.Count(logs.String(), "level=WARN"); got != 3 {
		t.Fatalf("got %d warnings, want one for unknown names and one per configured host:\n%s", got, &logs)
	}
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
