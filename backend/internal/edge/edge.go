// Package edge is the gatekeeper: the only process exposed to the internet.
// It accepts webhooks, checks them and hands them to the core; when the
// core is down it answers users by the maintenance rules.
package edge

import (
	"context"
	"log/slog"
	"os"
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
}

// New prepares the edge.
func New(cfg config.Edge, log *slog.Logger) (*Edge, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	e := &Edge{Cfg: cfg, Log: log, Mux: wire.NewMux()}
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
	e.Log.Info("edge started", "version", buildinfo.Version)
	<-ctx.Done()
	e.Log.Info("edge stopping")
	return nil
}
