package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

type env struct {
	ca   *pki.CA
	db   *store.DB
	w    *store.Writer
	srv  *Server
	addr string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	ca, err := pki.Open(ctx, filepath.Join(dir, "pki"), db)
	if err != nil {
		t.Fatal(err)
	}
	w := store.NewWriter(db, 20*time.Millisecond, nil)
	s := New(ca, w, slog.Default())
	cfg, err := s.TLSConfig("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: s.Handler(), ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug)}
	go hs.Serve(ln)
	t.Cleanup(func() { hs.Close(); w.Close(); db.Close() })
	return &env{ca: ca, db: db, w: w, srv: s, addr: ln.Addr().String()}
}

func (e *env) client(t *testing.T, kind pki.Kind, name string) *http.Client {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	var certs []tls.Certificate
	if kind != "" {
		cert, err := e.ca.Issue(context.Background(), kind, name, "", &k.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		certs = []tls.Certificate{{Certificate: [][]byte{cert.Raw}, PrivateKey: k, Leaf: cert}}
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: e.ca.Pool(), Certificates: certs, ServerName: "127.0.0.1",
	}}}
}

func TestNoCertificateNoHTTP(t *testing.T) {
	e := newEnv(t)
	_, err := e.client(t, "", "").Get("https://" + e.addr + "/api/ping")
	if err == nil {
		t.Fatal("request without a client certificate succeeded")
	}
	// Plain HTTP gets no HTTP answer either.
	c, err := net.Dial("tcp", e.addr)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("GET /api/ping HTTP/1.1\r\nHost: x\r\n\r\n"))
	c.SetReadDeadline(time.Now().Add(time.Second))
	b, _ := io.ReadAll(c)
	if strings.Contains(string(b), "HTTP/1.1 200") {
		t.Fatal("plain HTTP answered")
	}
}

func TestPingWithCertificate(t *testing.T) {
	e := newEnv(t)
	resp, err := e.client(t, pki.Owner, "home-pc").Get("https://" + e.addr + "/api/ping")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "home-pc") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	// last_seen is written by the batch writer.
	e.w.Flush(context.Background())
	var seen string
	e.db.R.QueryRow(`SELECT COALESCE(last_seen, '') FROM keys WHERE name = 'home-pc'`).Scan(&seen)
	if seen == "" {
		t.Fatal("last_seen not recorded")
	}
}

func TestEnrollmentOnlyForEnroll(t *testing.T) {
	e := newEnv(t)
	e.srv.Mux.HandleFunc("POST /enroll", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202) })
	c := e.client(t, pki.Enrollment, "new")
	resp, err := c.Get("https://" + e.addr + "/api/ping")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("ping with enrollment cert: %d", resp.StatusCode)
	}
	resp, err = c.Post("https://"+e.addr+"/enroll", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("enroll: %d", resp.StatusCode)
	}
}

func TestRevokedKeyDropsHandshake(t *testing.T) {
	e := newEnv(t)
	c := e.client(t, pki.Child, "laptop")
	resp, err := c.Get("https://" + e.addr + "/api/ping")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var serial string
	e.db.R.QueryRow(`SELECT serial FROM keys WHERE name = 'laptop'`).Scan(&serial)
	if err := e.ca.Revoke(context.Background(), serial); err != nil {
		t.Fatal(err)
	}
	c.Transport.(*http.Transport).CloseIdleConnections()
	if _, err := c.Get("https://" + e.addr + "/api/ping"); err == nil {
		t.Fatal("revoked key still works")
	}
}
