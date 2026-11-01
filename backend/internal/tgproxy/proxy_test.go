package tgproxy

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type env struct {
	p      *Proxy
	port   int
	client *http.Client
	mu     sync.Mutex
	calls  []Call
	conns  []Conn
}

func freePort(t *testing.T) int {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func newEnv(t *testing.T, upstream *httptest.Server, extraRoots *x509.CertPool) *env {
	t.Helper()
	ca, err := LoadCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{port: freePort(t)}
	e.p = &Proxy{CA: ca, Upstream: upstream.URL, UpstreamTLS: upstream.Client().Transport.(*http.Transport).TLSClientConfig,
		Hooks: Hooks{
			OnCall: func(c Call) { e.mu.Lock(); e.calls = append(e.calls, c); e.mu.Unlock() },
			OnConn: func(c Conn) { e.mu.Lock(); e.conns = append(e.conns, c); e.mu.Unlock() },
			Intercept: func(_ int64, m string) ([]byte, int, bool) {
				if m == "setWebhook" {
					return []byte(`{"ok":true,"result":true}`), 200, true
				}
				return nil, 0, false
			},
		}}
	if err := e.p.Start(1, "main", e.port); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.p.Stop(1, "main") })
	roots := extraRoots
	if roots == nil {
		roots = x509.NewCertPool()
	}
	roots.AddCert(ca.Cert)
	proxyURL, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(e.port))
	// What a bot with HTTPS_PROXY and SSL_CERT_FILE gets.
	e.client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots}}}
	return e
}

func fakeTelegram() *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			w.WriteHeader(429)
			io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":3}}`)
			return
		}
		io.WriteString(w, `{"ok":true,"result":{"id":1,"path":"`+r.URL.Path+`"}}`)
	}))
}

func TestInterceptsTelegram(t *testing.T) {
	up := fakeTelegram()
	defer up.Close()
	e := newEnv(t, up, nil)
	resp, err := e.client.Get("https://api.telegram.org/bot1:tok/getMe")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "/bot1:tok/getMe") {
		t.Fatalf("upstream answer = %s", b)
	}
	resp, _ = e.client.Post("https://api.telegram.org/bot1:tok/sendMessage", "application/json", strings.NewReader(`{}`))
	resp.Body.Close()
	resp, _ = e.client.Post("https://api.telegram.org/bot1:tok/setWebhook", "application/json", strings.NewReader(`{"url":"x"}`))
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != `{"ok":true,"result":true}` {
		t.Fatalf("setWebhook = %s", b)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.calls) != 3 || e.calls[0].Method != "getMe" || e.calls[1].ErrorCode != 429 || e.calls[1].RetryAfter != 3 || !e.calls[2].Local {
		t.Fatalf("calls = %+v", e.calls)
	}
}

func TestTunnelsOtherHosts(t *testing.T) {
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "payments ok") }))
	defer other.Close()
	pool := x509.NewCertPool()
	pool.AddCert(other.Certificate())
	up := fakeTelegram()
	defer up.Close()
	e := newEnv(t, up, pool)
	resp, err := e.client.Get(other.URL + "/pay")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	e.client.CloseIdleConnections()
	if string(b) != "payments ok" {
		t.Fatalf("body = %s", b)
	}
	// The tunnel reports the connection, without decrypting it.
	for range 100 {
		e.mu.Lock()
		n := len(e.conns)
		e.mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond) // the tunnel reports when both sides close
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	last := e.conns[len(e.conns)-1]
	if last.Host != other.Listener.Addr().String() || !last.Done || last.BytesIn == 0 {
		t.Fatalf("conns = %+v", e.conns)
	}
}

// The interception CA cannot vouch for any other site.
func TestNameConstraints(t *testing.T) {
	ca, err := LoadCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	for name, ok := range map[string]bool{TelegramHost: true, "bank.example": false, "evil.api.telegram.org.example": false} {
		leaf, err := ca.Sign(name)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := x509.ParseCertificate(leaf.Certificate[0])
		_, verr := c.Verify(x509.VerifyOptions{Roots: roots, DNSName: name})
		if (verr == nil) != ok {
			t.Errorf("%s: verify err = %v", name, verr)
		}
	}
}
