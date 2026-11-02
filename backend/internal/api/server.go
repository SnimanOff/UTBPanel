// Package api is the panel's HTTP API for the frontend and MCP. It is
// served only on the mTLS listener: without a client certificate signed by
// the panel's CA no HTTP response is ever sent.
package api

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// Source tells where a request came from.
type Source string

// Request sources, also used in the audit.
const (
	SourceUI  Source = "ui"
	SourceMCP Source = "mcp"
	SourceCLI Source = "cli"
)

// Caller is the authenticated client of a request.
type Caller struct {
	pki.Identity
	IP     string
	Source Source
}

// IsOwner reports whether the caller holds the owner key.
func (c Caller) IsOwner() bool { return c.Kind == pki.Owner }

type callerKey struct{}

// CallerFrom returns the caller of a request.
func CallerFrom(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c
}

// WithCaller attaches a caller to ctx.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerLog returns a logger tagged with the caller and path.
func CallerLog(r *http.Request) *slog.Logger {
	return slog.Default().With("key", CallerFrom(r.Context()).Name, "path", r.URL.Path)
}

// Server is the panel HTTP server.
type Server struct {
	CA  *pki.CA
	W   *store.Writer
	Log *slog.Logger
	Mux *Mux

	// Middleware wraps every API handler (CSRF checks, audit…), outermost first.
	middleware []func(http.Handler) http.Handler
}

// New returns a server with an empty route table.
func New(ca *pki.CA, w *store.Writer, log *slog.Logger) *Server {
	s := &Server{CA: ca, W: w, Log: log, Mux: NewMux()}
	s.Use(Guard)
	s.Mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) {
		c := CallerFrom(r.Context())
		WriteJSON(w, http.StatusOK, map[string]string{"pong": c.Name, "kind": string(c.Kind)})
	})
	return s
}

// Use adds middleware applied inside the authentication layer.
func (s *Server) Use(mw func(http.Handler) http.Handler) { s.middleware = append(s.middleware, mw) }

// TLSConfig returns the :443 TLS settings: TLS 1.3, client certificate
// required and checked against the registry on every handshake, so a
// revoked key stops working at once.
func (s *Server) TLSConfig(publicAddr string) (*tls.Config, error) {
	cert, key, err := s.CA.ServerCert(publicAddr)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// The CA goes in the chain: gate pins its public key.
		Certificates: []tls.Certificate{{Certificate: [][]byte{cert.Raw, s.CA.Cert().Raw}, PrivateKey: key, Leaf: cert}},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    s.CA.Pool(),
		NextProtos:   []string{"h2", "http/1.1"},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("client certificate required")
			}
			_, err := s.CA.Verify(cs.PeerCertificates[0])
			return err
		},
	}, nil
}

// Handler authenticates by client certificate and dispatches.
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.Mux
	for i := len(s.middleware) - 1; i >= 0; i-- {
		h = s.middleware[i](h)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		NoFraming(w.Header())
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "client certificate required", http.StatusUnauthorized)
			return
		}
		id, err := s.CA.Verify(r.TLS.PeerCertificates[0])
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		// An enrollment certificate is good only for asking for a device
		// certificate.
		if id.Kind == pki.Enrollment {
			ok := r.URL.Path == "/enroll" || strings.HasPrefix(r.URL.Path, "/enroll/")
			if id.Used {
				// Spent: it may only collect the approved certificate.
				ok = r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/enroll/")
			}
			if !ok {
				WriteError(w, http.StatusForbidden, "forbidden", "enrollment certificate is valid only for /enroll")
				return
			}
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		c := Caller{Identity: id, IP: ip, Source: SourceUI}
		if r.Header.Get("X-Panel-Source") == string(SourceMCP) {
			c.Source = SourceMCP
		}
		s.touch(c)
		h.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), c)))
	})
}

// touch records last_seen and last_ip in the next batch.
func (s *Server) touch(c Caller) {
	if s.W == nil || c.Kind == pki.Enrollment {
		return
	}
	now := store.Now()
	s.W.Later("seen:"+c.Serial, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE keys SET last_seen = ?, last_ip = ? WHERE serial = ?`, now, c.IP, c.Serial)
		return err
	})
}

// ListenAndServe serves the mTLS listener until ctx ends.
func (s *Server) ListenAndServe(ctx context.Context, addr, publicAddr string) error {
	cfg, err := s.TLSConfig(publicAddr)
	if err != nil {
		return err
	}
	ln, err := tls.Listen("tcp", addr, cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       5 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.Log.Handler(), slog.LevelDebug),
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
		srv.Close()
	}()
	s.Log.Info("mtls listener started", "addr", addr)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-stopped
	return nil
}

// ErrorBody is the single error format of the API. Telegram errors are
// passed as they are in Telegram.
type ErrorBody struct {
	Code     string          `json:"code"`
	Message  string          `json:"message"`
	Telegram json.RawMessage `json:"telegram,omitempty"`
}

// WriteJSON writes v as JSON.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// WriteError writes an error in the common format.
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	WriteJSON(w, status, ErrorBody{Code: code, Message: msg})
}
