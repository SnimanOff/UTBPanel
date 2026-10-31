// Package edge is the gatekeeper: the only process exposed to the internet.
// It accepts webhooks, checks them and hands them to the core; when the
// core is down it answers users by the maintenance rules.
package edge

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/buildinfo"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/safe"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Protocol versions spoken by the edge.
const (
	CoreVersion  = 1
	PanelVersion = 1
)

// Edge is the running gatekeeper.
type Edge struct {
	Cfg   config.Edge
	Log   *slog.Logger
	Mux   *wire.Mux
	Core  *wire.Client
	Panel *wire.Client

	telegram []netip.Prefix
	settings settings
	limiter  limiter
	certPEM  []byte

	cmu      sync.Mutex
	counters map[string]int64
}

func (e *Edge) count(name string) {
	e.cmu.Lock()
	e.counters[name]++
	e.cmu.Unlock()
}

// withoutCore answers when the core does not take the update; the rules
// of maintenance replies come next.
func (e *Edge) withoutCore(u Update) Reply { return Reply{Status: http.StatusOK} }

// New prepares the edge.
func New(cfg config.Edge, log *slog.Logger) (*Edge, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	e := &Edge{Cfg: cfg, Log: log, Mux: wire.NewMux(), telegram: cfg.TelegramPrefixes(),
		limiter: limiter{max: 60}, counters: map[string]int64{}}
	e.Core = &wire.Client{
		Path: wire.CoreSock(cfg.RunDir), Mux: e.Mux, Log: log,
		Hello:        app.Hello(wire.ProtoEdgeCore, "edge", CoreVersion, 1),
		OnConnect:    func(c *wire.Conn) { log.Info("connected to core", "peer", c.Peer().Hello.Name) },
		OnDisconnect: func(_ *wire.Conn, err error) { log.Warn("core connection lost", "err", err) },
	}
	e.Panel = &wire.Client{
		Path: wire.PanelSock(cfg.RunDir), Log: log,
		Hello:        app.Hello(wire.ProtoEdgePanel, "edge", PanelVersion, 1),
		OnConnect:    func(c *wire.Conn) { log.Info("connected to panel", "peer", c.Peer().Hello.Name) },
		OnDisconnect: func(_ *wire.Conn, err error) { log.Warn("panel connection lost", "err", err) },
	}
	return e, nil
}

// Run works until ctx is done.
func (e *Edge) Run(ctx context.Context) error {
	safe.Go(e.Log, "core client", func() { e.Core.Run(ctx) })
	safe.Go(e.Log, "panel client", func() { e.Panel.Run(ctx) })
	safe.Go(e.Log, "stats", func() { app.ReportStats(ctx, e.Core, "edge", 10*time.Second) })
	tlsCfg, pemBytes, acm, err := e.tlsSetup()
	if err != nil {
		return err
	}
	e.certPEM = pemBytes
	srv := &http.Server{
		Addr: e.Cfg.ListenTLS, Handler: e.handler(), TLSConfig: tlsCfg,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute,
		ErrorLog: slog.NewLogLogger(e.Log.Handler(), slog.LevelDebug),
	}
	errc := make(chan error, 2)
	safe.Go(e.Log, "tls listener", func() { errc <- srv.ListenAndServeTLS("", "") })
	var plain *http.Server
	if acm != nil {
		// :80 only answers Let's Encrypt challenges.
		plain = &http.Server{Addr: e.Cfg.ListenHTTP, Handler: acm.HTTPHandler(http.NotFoundHandler()), ReadHeaderTimeout: 10 * time.Second}
		safe.Go(e.Log, "http listener", func() { errc <- plain.ListenAndServe() })
	}
	e.Log.Info("edge started", "version", buildinfo.Version, "listen", e.Cfg.ListenTLS, "domain", e.Cfg.Domain)
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	e.Log.Info("edge stopping")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(sctx)
	if plain != nil {
		plain.Shutdown(sctx)
	}
	return nil
}
