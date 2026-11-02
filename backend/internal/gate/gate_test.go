package gate

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/devkey"
	"github.com/SnimanOff/UTBPanel/backend/internal/gatefile"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

// No TPM, Keychain or Secret Service on test machines.
func TestMain(m *testing.M) {
	devkey.FileKeysOnly()
	os.Exit(m.Run())
}

// fakePanel is the panel side of the tunnel: the real API server with the
// device routes plus a few test routes.
type fakePanel struct {
	t    *testing.T
	ca   *pki.CA
	addr string
	hs   *http.Server
	cfg  *tls.Config
	h    http.Handler
}

func newPanel(t *testing.T) *fakePanel {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ca, err := pki.Open(ctx, filepath.Join(dir, "pki"), db)
	if err != nil {
		t.Fatal(err)
	}
	s := api.New(ca, nil, slog.Default())
	(&api.Devices{CA: ca}).Register(s.Mux)
	s.Mux.HandleFunc("GET /api/whoami", func(w http.ResponseWriter, r *http.Request) {
		c := api.CallerFrom(r.Context())
		fmt.Fprintf(w, "%s %s %s cookie=%q auth=%q", c.Name, c.Kind, c.Source, r.Header.Get("Cookie"), r.Header.Get("Authorization"))
	})
	s.Mux.HandleFunc("GET /mcp", func(w http.ResponseWriter, r *http.Request) {
		c := api.CallerFrom(r.Context())
		fmt.Fprintf(w, "%s %s %s auth=%q", c.Name, c.Kind, c.Source, r.Header.Get("Authorization"))
	})
	s.Mux.HandleFunc("GET /api/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select { // the client must see "first" before the handler ends
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
		fmt.Fprint(w, "data: last\n\n")
	})
	s.Mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, api.CallerFrom(r.Context()).Source) })
	s.Mux.HandleFunc("/api/bots/{id}/releases", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, api.CallerFrom(r.Context()).Source) })
	s.Mux.HandleFunc("GET /api/notifications/stream", func(w http.ResponseWriter, r *http.Request) {
		sse := api.NewSSE(w)
		sse.Send("7", "notification", map[string]any{"id": 7, "text": "Echo: упал (код 2)", "severity": "error", "href": "/bots/1/logs"})
		<-r.Context().Done()
	})
	s.Mux.HandleFunc("POST /api/upload", func(w http.ResponseWriter, r *http.Request) {
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		part, err := mr.NextPart()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		n, _ := io.Copy(io.Discard, part)
		fmt.Fprint(w, n)
	})
	cfg, err := s.TLSConfig("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePanel{t: t, ca: ca, cfg: cfg, h: s.Handler()}
	p.start("127.0.0.1:0")
	t.Cleanup(func() { p.hs.Close() })
	return p
}

func (p *fakePanel) start(addr string) {
	var ln net.Listener
	var err error
	for range 50 { // the port may take a moment to free after a restart
		if ln, err = tls.Listen("tcp", addr, p.cfg); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		p.t.Fatal(err)
	}
	p.addr = ln.Addr().String()
	p.hs = &http.Server{Handler: p.h, ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug)}
	go p.hs.Serve(ln)
}

func freePort(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type setup struct {
	panel   *fakePanel
	gate    *Gate
	cfg     Config
	base    string
	browser *http.Client
	cancel  context.CancelFunc
}

// enrolled starts a panel and a gate, registers the gate and logs a
// browser in.
func enrolled(t *testing.T) *setup {
	t.Helper()
	p := newPanel(t)
	block, err := p.ca.NewGate(context.Background(), pki.Owner, "home-pc", []string{p.addr})
	if err != nil {
		t.Fatal(err)
	}
	listen := freePort(t)
	opened := make(chan string, 4)
	cfg := Config{
		Block: block, StateDir: t.TempDir(), Listen: listen, NoTPM: true,
		PollInterval: 30 * time.Millisecond,
		OpenBrowser:  func(u string) { opened <- u },
	}
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go g.Run(ctx)
	waitFor(t, "enrollment request", func() bool { return g.EnrollState().State == "waiting" })

	// The owner sees the same code in the panel and approves it.
	reqs, err := p.ca.PendingDevices(context.Background())
	if err != nil || len(reqs) != 1 {
		t.Fatalf("pending = %v, %v", reqs, err)
	}
	if reqs[0].Code != g.EnrollState().Code || len(strings.Fields(reqs[0].Code)) != 4 {
		t.Fatalf("codes differ: panel %q, gate %q", reqs[0].Code, g.EnrollState().Code)
	}
	if _, err := p.ca.Approve(context.Background(), reqs[0].ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "device certificate", g.Enrolled)

	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	login := <-opened
	resp, err := browser.Get(login)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return &setup{panel: p, gate: g, cfg: cfg, base: "http://" + listen, browser: browser, cancel: cancel}
}

func get(t *testing.T, c *http.Client, url string, hdr ...string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestEnrollAndProxy(t *testing.T) {
	s := enrolled(t)
	code, body := get(t, s.browser, s.base+"/api/whoami")
	if code != 200 || !strings.HasPrefix(body, "home-pc owner ui") {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(body, `cookie=""`) {
		t.Fatalf("gate leaked its cookie to the panel: %s", body)
	}

	// Another program without the cookie.
	code, _ = get(t, http.DefaultClient, s.base+"/api/whoami")
	if code != http.StatusForbidden {
		t.Fatalf("no cookie: %d", code)
	}
	// A used one-time link does not work twice.
	login := s.gate.LoginURL()
	jar, _ := cookiejar.New(nil)
	other := &http.Client{Jar: jar}
	get(t, other, login)
	jar2, _ := cookiejar.New(nil)
	if code, _ := get(t, &http.Client{Jar: jar2}, login); code != http.StatusForbidden {
		t.Fatalf("login link reused: %d", code)
	}

	// MCP client with its token.
	code, body = get(t, http.DefaultClient, s.base+"/mcp", "Authorization", "Bearer "+s.gate.MCPToken())
	if code != 200 || !strings.HasPrefix(body, "home-pc owner mcp") || !strings.Contains(body, `auth=""`) {
		t.Fatalf("mcp: %d %s", code, body)
	}
	code, _ = get(t, http.DefaultClient, s.base+"/api/whoami", "Authorization", "Bearer "+s.gate.MCPToken())
	if code != http.StatusForbidden {
		t.Fatal("MCP token bypassed the tool policy through the REST API", code)
	}
	code, _ = get(t, s.browser, s.base+"/mcp")
	if code != http.StatusForbidden {
		t.Fatal("browser session accessed MCP without its token", code)
	}
	req, _ := http.NewRequest("POST", s.base+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+s.gate.MCPToken())
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("MCP client without browser CSRF headers was rejected", response.StatusCode)
	}
	// A local build is uploaded with the same token, and nothing else.
	upload := func(method, path string) (int, string) {
		req, _ := http.NewRequest(method, s.base+path, strings.NewReader("bin"))
		req.Header.Set("Authorization", "Bearer "+s.gate.MCPToken())
		req.Header.Set("Content-Type", "multipart/form-data; boundary=x") // curl -F
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := upload("POST", "/api/bots/3/releases"); code != 200 || body != "mcp" {
		t.Fatalf("upload with the MCP token: %d %s", code, body)
	}
	for _, c := range [][2]string{{"GET", "/api/bots/3/releases"}, {"POST", "/api/bots/3/releases/v1/switch"}, {"POST", "/api/bots/3/releases/"}, {"POST", "/api/bots/x/releases"}} {
		if code, _ := upload(c[0], c[1]); code != http.StatusForbidden {
			t.Errorf("%s %s with the MCP token: %d", c[0], c[1], code)
		}
	}
	code, _ = get(t, http.DefaultClient, s.base+"/api/whoami", "Authorization", "Bearer wrong")
	if code != http.StatusForbidden {
		t.Fatalf("wrong token: %d", code)
	}
}

func TestHostCheck(t *testing.T) {
	s := enrolled(t)
	req, _ := http.NewRequest("GET", s.base+"/api/whoami", nil)
	req.Host = "evil.example:" + s.gate.port
	resp, err := s.browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rebinding host: %d", resp.StatusCode)
	}
}

func TestSecondStartDoesNotEnrollAgain(t *testing.T) {
	s := enrolled(t)
	g2, err := New(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !g2.Enrolled() {
		t.Fatal("the same gate.exe started again must not register again")
	}
	if g2.MCPToken() != s.gate.MCPToken() {
		t.Fatal("MCP token changed between starts")
	}
	// The enrollment certificate is spent.
	reqs, _ := s.panel.ca.PendingDevices(context.Background())
	if len(reqs) != 0 {
		t.Fatalf("pending after approval: %d", len(reqs))
	}
}

func TestSSEUnbuffered(t *testing.T) {
	s := enrolled(t)
	resp, err := s.browser.Get(s.base + "/api/sse")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		if line != "data: first\n" {
			t.Fatalf("line = %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SSE event was buffered by the gate")
	}
}

func TestLargeUploadStreams(t *testing.T) {
	s := enrolled(t)
	const size = 64 << 20
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, _ := mw.CreateFormFile("file", "bot")
		chunk := make([]byte, 1<<20)
		for range size / len(chunk) {
			part.Write(chunk)
		}
		mw.Close()
		pw.Close()
	}()
	req, _ := http.NewRequest("POST", s.base+"/api/upload", pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Panel", "1")
	resp, err := s.browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != fmt.Sprint(size) {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

func TestReconnectAfterPanelRestart(t *testing.T) {
	s := enrolled(t)
	if code, _ := get(t, s.browser, s.base+"/api/whoami"); code != 200 {
		t.Fatal(code)
	}
	addr := s.panel.addr
	s.panel.hs.Close()
	if code, body := get(t, s.browser, s.base+"/api/whoami"); code != http.StatusBadGateway || !strings.Contains(body, "Сервер недоступен") {
		t.Fatalf("panel down: %d %s", code, body)
	}
	s.panel.start(addr)
	if code, _ := get(t, s.browser, s.base+"/api/whoami"); code != 200 {
		t.Fatalf("after restart: %d", code)
	}
}

func TestRevokedDeviceStopsWorking(t *testing.T) {
	s := enrolled(t)
	keys, _ := s.panel.ca.List(context.Background())
	for _, k := range keys {
		if k.Kind == pki.Owner {
			s.panel.ca.Revoke(context.Background(), k.Serial)
		}
	}
	if code, _ := get(t, s.browser, s.base+"/api/whoami"); code == 200 {
		t.Fatal("revoked device still works")
	}
}

func TestNoBlock(t *testing.T) {
	if _, err := New(Config{}); err != gatefile.ErrNoBlock {
		t.Fatalf("got %v", err)
	}
}

func TestWatchNotifications(t *testing.T) {
	s := enrolled(t)
	got := make(chan Note, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.gate.Watch(ctx, func(n Note) { got <- n }, nil)
	select {
	case n := <-got:
		if n.ID != 7 || n.Severity != "error" || n.Href != "/bots/1/logs" {
			t.Fatalf("%+v", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no notification through the tunnel")
	}

	// The server page changes the address (browser with the session).
	resp, err := s.browser.PostForm(s.base+"/__gate/server", map[string][]string{"addr": {"bad address"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "host:port") || s.gate.Server() != s.panel.addr {
		t.Fatalf("bad address accepted: %s", b)
	}
	// login ?next= stays on this host.
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r2, err := c.Get(s.gate.LoginURL() + "&next=//evil.example/x")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if loc := r2.Header.Get("Location"); loc != "/" {
		t.Fatalf("redirect to %q", loc)
	}
}
