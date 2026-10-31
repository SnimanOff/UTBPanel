// Package panel wires the panel process: panel.db, the connection to the
// core, the socket for the edge, and the HTTP API.
package panel

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/buildinfo"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/safe"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Protocol versions spoken by the panel.
const (
	CoreVersion = 1
	EdgeVersion = 1
)

// Panel is the running panel process.
type Panel struct {
	Cfg     config.Panel
	Log     *slog.Logger
	DB      *store.DB
	W       *store.Writer
	CoreMux *wire.Mux
	Core    *wire.Client
	EdgeMux *wire.Mux
	EdgeSrv *wire.Server
	Events  *events.Sink
	CA      *pki.CA
	API     *api.Server

	closers []func(context.Context) error
	runners []func(context.Context)
	onCore  []func(*wire.Conn)
}

// New opens panel.db and prepares connections.
func New(ctx context.Context, cfg config.Panel, log *slog.Logger) (*Panel, error) {
	for _, d := range []string{cfg.DataDir, cfg.RunDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, "panel.db"), migrations.Panel())
	if err != nil {
		return nil, err
	}
	ca, err := pki.Open(ctx, filepath.Join(cfg.DataDir, "pki"), db)
	if err != nil {
		db.Close()
		return nil, err
	}
	p := &Panel{
		Cfg: cfg, Log: log, DB: db,
		W:       store.NewWriter(db, 10*time.Second, log),
		CoreMux: wire.NewMux(),
		EdgeMux: wire.NewMux(),
		CA:      ca,
	}
	p.API = api.New(ca, p.W, log)
	(&api.Keys{CA: ca, GateExe: cfg.GateExe, Servers: p.GateServers}).Register(p.API.Mux)
	p.Events = events.NewSink(db, p.CoreMux, log)
	p.Core = &wire.Client{
		Path: wire.CoreSock(cfg.RunDir), Mux: p.CoreMux, Log: log,
		Hello: app.Hello(wire.ProtoCorePanel, "panel", CoreVersion, 1),
		OnConnect: func(c *wire.Conn) {
			log.Info("connected to core", "peer", c.Peer().Hello.Name)
			for _, fn := range p.onCore {
				safe.Go(log, "on core connect", func() { fn(c) })
			}
		},
		OnDisconnect: func(_ *wire.Conn, err error) { log.Warn("core connection lost", "err", err) },
	}
	p.EdgeSrv = &wire.Server{
		Mux: p.EdgeMux, Log: log,
		Hello: app.Hello(wire.ProtoEdgePanel, "panel", EdgeVersion, 1),
		Also:  []wire.Hello{app.Hello(wire.ProtoPanelCLI, "panel", 1, 1)},
	}
	p.EdgeMux.Handle("keys.reload", func(ctx context.Context, _ *wire.Conn, _ wire.Raw) (any, error) {
		return nil, p.CA.Reload(ctx)
	})
	p.OnCoreConnect(func(c *wire.Conn) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := p.Events.CatchUp(ctx, c); err != nil {
			log.Warn("fetching missed core events failed", "err", err)
		}
	})
	return p, nil
}

// GateServers returns the addresses written into gate.exe.
func (p *Panel) GateServers() []string { return GateServers(p.Cfg) }

// GateServers returns the panel addresses for gate.exe from the config.
func GateServers(cfg config.Panel) []string {
	_, port, _ := net.SplitHostPort(cfg.Listen)
	if port == "" {
		port = "443"
	}
	return []string{net.JoinHostPort(cfg.PublicAddr, port)}
}

// OnCoreConnect runs fn (in its own goroutine) after every connect to the core.
func (p *Panel) OnCoreConnect(fn func(*wire.Conn)) { p.onCore = append(p.onCore, fn) }

// OnClose registers a shutdown step.
func (p *Panel) OnClose(fn func(context.Context) error) { p.closers = append(p.closers, fn) }

// Go registers a background task started by Run.
func (p *Panel) Go(fn func(context.Context)) { p.runners = append(p.runners, fn) }

// Run works until ctx is done.
func (p *Panel) Run(ctx context.Context) error {
	ln, err := wire.Listen(wire.PanelSock(p.Cfg.RunDir), 0o660, p.Cfg.SocketGID)
	if err != nil {
		return fmt.Errorf("panel socket: %w", err)
	}
	safe.Go(p.Log, "panel socket", func() { p.EdgeSrv.Serve(ln) })
	safe.Go(p.Log, "core client", func() { p.Core.Run(ctx) })
	safe.Go(p.Log, "stats", func() { app.ReportStats(ctx, p.Core, "panel", 10*time.Second) })
	safe.Go(p.Log, "mtls listener", func() {
		if err := p.API.ListenAndServe(ctx, p.Cfg.Listen, p.Cfg.PublicAddr); err != nil {
			p.Log.Error("mtls listener stopped", "err", err)
		}
	})
	for _, fn := range p.runners {
		safe.Go(p.Log, "panel task", func() { fn(ctx) })
	}
	p.Log.Info("panel started", "version", buildinfo.Version)
	<-ctx.Done()
	p.Log.Info("panel stopping")
	ln.Close()
	steps := make([]func(context.Context) error, 0, len(p.closers)+1)
	for i := len(p.closers) - 1; i >= 0; i-- {
		steps = append(steps, p.closers[i])
	}
	steps = append(steps, func(context.Context) error { p.W.Close(); return p.DB.Close() })
	app.Shutdown(p.Log, 15*time.Second, steps...)
	return nil
}
