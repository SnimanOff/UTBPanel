package edge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

const telegramIP = "149.154.167.220"

type fakeCore struct {
	got  atomic.Int64
	ln   interface{ Close() error }
	srv  *wire.Server
	path string
}

func startCore(t *testing.T, dir string, reply func(Update) Reply) *fakeCore {
	t.Helper()
	fc := &fakeCore{path: wire.CoreSock(dir)}
	mux := wire.NewMux()
	mux.Handle(MethodUpdate, wire.Func(func(_ context.Context, _ *wire.Conn, u Update) (Reply, error) {
		fc.got.Add(1)
		return reply(u), nil
	}))
	mux.Handle("ping", func(context.Context, *wire.Conn, wire.Raw) (any, error) { return "pong", nil })
	fc.srv = &wire.Server{Mux: mux, Hello: wire.Hello{Proto: wire.ProtoEdgeCore, Version: 1}}
	ln, err := wire.Listen(fc.path, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	fc.ln = ln
	go fc.srv.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return fc
}

func newEdge(t *testing.T) (*Edge, string) {
	t.Helper()
	run, _ := os.MkdirTemp("", "edge")
	t.Cleanup(func() { os.RemoveAll(run) })
	cfg := config.DefaultEdge()
	cfg.DataDir, cfg.RunDir = filepath.Join(t.TempDir(), "edge"), run
	cfg.PublicAddr = "203.0.113.10"
	e, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.settings.setSecrets(map[int64]string{7: "s3cret"})
	return e, run
}

func post(e *Edge, path, ip, secret, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.RemoteAddr = ip + ":443"
	if secret != "" {
		r.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	}
	w := httptest.NewRecorder()
	e.handler().ServeHTTP(w, r)
	return w
}

func TestFilters(t *testing.T) {
	e, run := newEdge(t)
	fc := startCore(t, run, func(u Update) Reply {
		return Reply{Status: 200, Body: []byte(`{"method":"sendMessage","chat_id":1,"text":"hi"}`)}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Core.Run(ctx)
	if _, err := e.Core.Wait(ctxTimeout(t)); err != nil {
		t.Fatal(err)
	}

	if w := post(e, "/hook/7", "203.0.113.5", "s3cret", "{}"); w.Code != http.StatusForbidden {
		t.Fatalf("foreign IP: %d", w.Code)
	}
	if w := post(e, "/hook/7", telegramIP, "wrong", "{}"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: %d", w.Code)
	}
	if w := post(e, "/hook/8", telegramIP, "s3cret", "{}"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown bot: %d", w.Code)
	}
	w := post(e, "/hook/7", telegramIP, "s3cret", `{"update_id":1}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "sendMessage") || fc.got.Load() != 1 {
		t.Fatalf("good update: %d %s", w.Code, w.Body)
	}
	big := strings.Repeat("x", int(e.Cfg.MaxBody)+1)
	if w := post(e, "/hook/7", telegramIP, "s3cret", big); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("big body: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/api/bots", nil)
	r.RemoteAddr = "198.51.100.1:1"
	rec := httptest.NewRecorder()
	e.handler().ServeHTTP(rec, r)
	if rec.Code != 404 || rec.Body.Len() != 0 {
		t.Fatalf("no UI on the public port: %d", rec.Code)
	}
}

func TestScannerRateLimit(t *testing.T) {
	e, _ := newEdge(t)
	var last int
	for range 100 {
		r := httptest.NewRequest("GET", "/wp-login.php", nil)
		r.RemoteAddr = "198.51.100.9:1"
		w := httptest.NewRecorder()
		e.handler().ServeHTTP(w, r)
		last = w.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("got %d", last)
	}
}

func TestSelfSigned(t *testing.T) {
	dir := t.TempDir()
	c1, pem1, err := selfSigned(dir, "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	_, pem2, _ := selfSigned(dir, "203.0.113.10")
	if string(pem1) != string(pem2) || c1 == nil {
		t.Fatal("certificate recreated on the second start")
	}
}

func ctxTimeout(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
