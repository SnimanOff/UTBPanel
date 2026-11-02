// Package gate is the logic of gate.exe: a local HTTP proxy on
// 127.0.0.1 that lets only the browser session it opened (or an MCP client
// with its token) through, and forwards requests to the panel over mTLS with
// the device key.
package gate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/devkey"
	"github.com/SnimanOff/UTBPanel/backend/internal/gatefile"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
)

// DefaultListen is the local address of the gate.
const DefaultListen = "127.0.0.1:47100"

// CookieName is the session cookie set by the one-time link.
const CookieName = "tgpanel_gate"

const loginTTL = 60 * time.Second

// Config configures a gate.
type Config struct {
	Block    *gatefile.Block
	StateDir string // usually %APPDATA%\tgpanel
	Listen   string
	NoTPM    bool
	Log      *slog.Logger
	// OpenBrowser opens a URL; nil means do nothing.
	OpenBrowser func(url string)
	// PollInterval of the enrollment status; 3 s when zero.
	PollInterval time.Duration
}

// Gate is a running gate.
type Gate struct {
	cfg  Config
	log  *slog.Logger
	dir  string
	port string

	mu       sync.Mutex
	key      devkey.Key
	device   *tls.Certificate
	server   string
	sessions map[string]time.Time
	logins   map[string]time.Time
	mcpToken string
	enroll   EnrollState
	proxy    *httputil.ReverseProxy
	tr       *http.Transport
}

// EnrollState is shown on the local enrollment page.
type EnrollState struct {
	State string `json:"state"` // starting | waiting | approved | rejected | expired | error
	Code  string `json:"code,omitempty"`
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
}

// New loads the gate state.
func New(cfg Config) (*Gate, error) {
	if cfg.Block == nil {
		return nil, gatefile.ErrNoBlock
	}
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 3 * time.Second
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, err
	}
	enroll, err := x509.ParseCertificate(cfg.Block.EnrollCert)
	if err != nil {
		return nil, fmt.Errorf("gate: bad enrollment certificate: %w", err)
	}
	// One directory per issued gate: a new gate.exe for the same name
	// registers anew, the same gate.exe started again does not.
	serial := pki.SerialString(enroll.SerialNumber)
	dir := filepath.Join(cfg.StateDir, cfg.Block.KeyName+"-"+serial[:min(8, len(serial))])
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	g := &Gate{
		cfg: cfg, log: cfg.Log, dir: dir, port: port,
		server:   cfg.Block.Servers[0],
		sessions: map[string]time.Time{},
		logins:   map[string]time.Time{},
		enroll:   EnrollState{State: "starting", Name: cfg.Block.KeyName},
	}
	if b, err := os.ReadFile(filepath.Join(dir, "server")); err == nil && strings.TrimSpace(string(b)) != "" {
		g.server = strings.TrimSpace(string(b))
	}
	if err := g.loadMCPToken(); err != nil {
		return nil, err
	}
	if err := g.loadDevice(); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, devkey.ErrNoKey) {
		return nil, err
	}
	return g, nil
}

// Dir returns the state directory of this gate.
func (g *Gate) Dir() string { return g.dir }

// Enrolled reports whether the device has its certificate.
func (g *Gate) Enrolled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.device != nil
}

// Server returns the panel address in use.
func (g *Gate) Server() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.server
}

// SetServer changes the panel address (the server moved).
func (g *Gate) SetServer(addr string) error {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("gate: address must be host:port: %w", err)
	}
	if err := os.WriteFile(filepath.Join(g.dir, "server"), []byte(addr), 0o600); err != nil {
		return err
	}
	g.mu.Lock()
	g.server = addr
	if g.device != nil {
		g.buildProxy()
	}
	g.mu.Unlock()
	return nil
}

// MCPToken returns the token for MCP clients.
func (g *Gate) MCPToken() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.mcpToken
}

// RotateMCPToken replaces the MCP token.
func (g *Gate) RotateMCPToken() (string, error) {
	t := randomToken()
	if err := os.WriteFile(filepath.Join(g.dir, "mcp.token"), []byte(t), 0o600); err != nil {
		return "", err
	}
	g.mu.Lock()
	g.mcpToken = t
	g.mu.Unlock()
	return t, nil
}

func (g *Gate) loadMCPToken() error {
	b, err := os.ReadFile(filepath.Join(g.dir, "mcp.token"))
	if err == nil && len(b) > 0 {
		g.mcpToken = strings.TrimSpace(string(b))
		return nil
	}
	_, err = g.RotateMCPToken()
	return err
}

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (g *Gate) loadDevice() error {
	certPEM, err := os.ReadFile(filepath.Join(g.dir, "device.crt"))
	if err != nil {
		return err
	}
	key, err := devkey.Open(g.dir)
	if err != nil {
		return err
	}
	blk, _ := pem.Decode(certPEM)
	if blk == nil {
		return errors.New("gate: device.crt is not PEM")
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.key = key
	g.device = &tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: key, Leaf: leaf}
	g.enroll.State = "approved"
	g.buildProxy()
	g.mu.Unlock()
	return nil
}

// pinnedVerify checks the server chain against the CA public key pinned in
// gate.exe instead of system roots: no domain needed, no way to substitute
// the server.
func pinnedVerify(fingerprint []byte) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) < 2 {
			return errors.New("gate: server sent no CA certificate")
		}
		leaf, err := x509.ParseCertificate(raw[0])
		if err != nil {
			return err
		}
		for _, r := range raw[1:] {
			ca, err := x509.ParseCertificate(r)
			if err != nil {
				continue
			}
			h := sha256.Sum256(ca.RawSubjectPublicKeyInfo)
			if subtle.ConstantTimeCompare(h[:], fingerprint) != 1 {
				continue
			}
			if err := leaf.CheckSignatureFrom(ca); err != nil {
				return fmt.Errorf("gate: server certificate not signed by the panel CA: %w", err)
			}
			now := time.Now()
			if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
				return errors.New("gate: server certificate expired")
			}
			return nil
		}
		return errors.New("gate: server CA does not match the pinned fingerprint")
	}
}

func (g *Gate) tlsConfig(cert *tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:            tls.VersionTLS13,
		Certificates:          []tls.Certificate{*cert},
		InsecureSkipVerify:    true, // replaced by the pinned check below
		VerifyPeerCertificate: pinnedVerify(g.cfg.Block.CAFingerprint),
	}
}

// buildProxy prepares forwarding; call with g.mu held.
func (g *Gate) buildProxy() {
	if g.tr != nil {
		g.tr.CloseIdleConnections()
	}
	// One long-lived HTTP/2 connection: a TPM signature takes about 50 ms,
	// so a handshake per request would be noticeable.
	g.tr = &http.Transport{
		TLSClientConfig:     g.tlsConfig(g.device),
		ForceAttemptHTTP2:   true,
		IdleConnTimeout:     30 * time.Minute,
		MaxIdleConnsPerHost: 4,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	target := &url.URL{Scheme: "https", Host: g.server}
	g.proxy = &httputil.ReverseProxy{
		Transport:     g.tr,
		FlushInterval: -1, // SSE goes through at once
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host // the panel checks Host too (DNS rebinding)
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("X-Panel-Source")
			if bearer(pr.In) != "" {
				pr.Out.Header.Set("X-Panel-Source", "mcp")
				pr.Out.Header.Set("X-Panel", "1")
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			g.log.Warn("panel unreachable", "server", target.Host, "err", err)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, unavailablePage, target.Host)
		},
	}
}

// LoginURL makes a one-time link that opens a browser session.
func (g *Gate) LoginURL() string {
	t := randomToken()
	g.mu.Lock()
	g.logins[t] = time.Now().Add(loginTTL)
	g.mu.Unlock()
	return "http://127.0.0.1:" + g.port + "/__gate/login?t=" + t
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// allowedHost protects against DNS rebinding: a foreign site resolving its
// name to 127.0.0.1 still sends its own name in Host.
func (g *Gate) allowedHost(host string) bool {
	return host == "127.0.0.1:"+g.port || host == "localhost:"+g.port
}

type authKind int

const (
	authNone authKind = iota
	authSession
	authMCP
)

func (g *Gate) auth(r *http.Request) authKind {
	g.mu.Lock()
	defer g.mu.Unlock()
	if t := bearer(r); t != "" {
		if subtle.ConstantTimeCompare([]byte(t), []byte(g.mcpToken)) == 1 {
			return authMCP
		}
		return authNone
	}
	if c, err := r.Cookie(CookieName); err == nil {
		if _, ok := g.sessions[c.Value]; ok {
			return authSession
		}
	}
	return authNone
}

// Handler is the local HTTP server.
func (g *Gate) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.allowedHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/__gate/login" {
			g.login(w, r)
			return
		}
		a := g.auth(r)
		if a == authNone {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, forbiddenPage)
			return
		}
		if (a == authMCP && r.URL.Path != "/mcp") || (a != authMCP && r.URL.Path == "/mcp") {
			http.Error(w, "MCP token is required and is valid only for /mcp", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/__gate/") {
			g.local(w, r)
			return
		}
		g.mu.Lock()
		proxy := g.proxy
		g.mu.Unlock()
		if proxy == nil {
			if a == authMCP {
				http.Error(w, "gate is not registered yet", http.StatusServiceUnavailable)
				return
			}
			http.Redirect(w, r, "/__gate/enroll", http.StatusFound)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

func (g *Gate) login(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("t")
	g.mu.Lock()
	exp, ok := g.logins[t]
	delete(g.logins, t) // single use
	for k, e := range g.logins {
		if time.Now().After(e) {
			delete(g.logins, k)
		}
	}
	var sid string
	if ok && time.Now().Before(exp) {
		sid = randomToken()
		g.sessions[sid] = time.Now()
	}
	g.mu.Unlock()
	if sid == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, forbiddenPage)
		return
	}
	// HttpOnly: cookies are not bound to a port, so without it a script of
	// any other page on 127.0.0.1 could read it.
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	next := "/"
	// Only gate pages and panel paths: never another host.
	if n := r.URL.Query().Get("next"); strings.HasPrefix(n, "/") && !strings.HasPrefix(n, "//") {
		next = n
	}
	http.Redirect(w, r, next, http.StatusFound)
}

// OpenPanel opens the browser with a fresh one-time link.
func (g *Gate) OpenPanel() {
	if g.cfg.OpenBrowser != nil {
		g.cfg.OpenBrowser(g.LoginURL())
	}
}

// ErrPortBusy means another program holds the local port.
var ErrPortBusy = errors.New("gate: the local port is busy")

// Run serves the local port until ctx ends; it registers the device first
// if needed.
func (g *Gate) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", g.cfg.Listen)
	if err != nil {
		return fmt.Errorf("%w (%s): %v", ErrPortBusy, g.cfg.Listen, err)
	}
	srv := &http.Server{Handler: g.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	if !g.Enrolled() {
		go func() {
			if err := g.Enroll(ctx); err != nil && ctx.Err() == nil {
				g.log.Error("enrollment failed", "err", err)
			}
		}()
	}
	g.OpenPanel()
	g.log.Info("gate started", "listen", g.cfg.Listen, "server", g.Server(), "enrolled", g.Enrolled())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

const forbiddenPage = `<!doctype html><meta charset="utf-8"><title>TG Panel</title>
<body style="background:#0c0a0f;color:#e9e4ee;font:16px sans-serif;padding:40px">
<p>Нет доступа. Откройте панель через gate.exe: двойной клик или значок в трее.</p>`

const unavailablePage = `<!doctype html><meta charset="utf-8"><title>TG Panel</title>
<body style="background:#0c0a0f;color:#e9e4ee;font:16px sans-serif;padding:40px">
<p style="color:#c8102e">Сервер недоступен: %s</p><p>gate попробует снова при следующем запросе.</p>`
