// Package panel wires the panel process: panel.db, the connection to the
// core, the socket for the edge, and the HTTP API.
package panel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/aidocs"
	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/audit"
	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/bots"
	"github.com/SnimanOff/UTBPanel/backend/internal/buildinfo"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/counters"
	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/edge"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/livehub"
	"github.com/SnimanOff/UTBPanel/backend/internal/mcp"
	"github.com/SnimanOff/UTBPanel/backend/internal/metrics"
	"github.com/SnimanOff/UTBPanel/backend/internal/panics"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/procstat"
	"github.com/SnimanOff/UTBPanel/backend/internal/safe"
	"github.com/SnimanOff/UTBPanel/backend/internal/secretreq"
	"github.com/SnimanOff/UTBPanel/backend/internal/servicebot"
	"github.com/SnimanOff/UTBPanel/backend/internal/settings"
	"github.com/SnimanOff/UTBPanel/backend/internal/sources"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	"github.com/SnimanOff/UTBPanel/backend/internal/users"
	"github.com/SnimanOff/UTBPanel/backend/internal/webaccess"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Protocol versions spoken by the panel.
const (
	CoreVersion = 1
	EdgeVersion = 1
)

// Panel is the running panel process.
type Panel struct {
	restart         chan app.RestartRequest
	restoring       atomic.Bool
	restoreUploads  *backup.Staging
	restoreMu       sync.Mutex
	restorePrepared map[string]string
	backupPartsMu   sync.Mutex
	Cfg             config.Panel
	Log             *slog.Logger
	DB              *store.DB
	W               *store.Writer
	CoreMux         *wire.Mux
	Core            *wire.Client
	EdgeMux         *wire.Mux
	EdgeSrv         *wire.Server
	Events          *events.Sink
	CA              *pki.CA
	API             *api.Server
	Bots            *bots.Service
	Audit           *audit.Log
	Metrics         *metrics.Storage
	Settings        *settings.Store

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
	if err := backup.RecoverInstall(cfg.DataDir); err != nil {
		return nil, err
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
		restart: make(chan app.RestartRequest, 1),
		Cfg:     cfg, Log: log, DB: db,
		W:       store.NewWriter(db, 10*time.Second, log),
		CoreMux: wire.NewMux(),
		EdgeMux: wire.NewMux(),
		CA:      ca,
	}
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
	p.API = api.New(ca, p.W, log)
	p.Audit = &audit.Log{DB: db, W: p.W, Log: log}
	p.API.Use(p.Audit.Middleware)
	p.Audit.Register(p.API.Mux)
	p.API.Mux.ServeMux.Handle("/", api.Static(cfg.WebDir))
	annotate := func(r *http.Request, action, object string, details map[string]any) {
		audit.Annotate(r, action, object, details)
	}
	p.Bots = &bots.Service{Core: p.Core, Groups: &bots.Groups{DB: db}}
	p.Bots.Register(p.API.Mux)
	(&sources.Uploads{Core: p.Core}).Register(p.API.Mux)
	(&api.Keys{CA: ca, GateExe: cfg.GateExe, Servers: p.GateServers, Audit: annotate}).Register(p.API.Mux)
	(&api.Devices{CA: ca, Audit: annotate, OnNew: func(d pki.DeviceRequest) {
		log.Info("new device waiting for approval", "name", d.Name, "kind", d.Kind, "ip", d.IP, "code", d.Code)
	}}).Register(p.API.Mux)
	p.Go(func(ctx context.Context) {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if err := ca.PruneDevices(ctx); err != nil && ctx.Err() == nil {
				log.Warn("pruning device requests failed", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	})
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
	// Minutes of metrics: fetched from the core, written, then confirmed.
	p.Metrics = &metrics.Storage{DB: db, W: p.W, Log: log}
	(&metrics.API{S: p.Metrics, Core: p.Core}).Register(p.API.Mux)
	(&metrics.ServicesAPI{S: p.Metrics}).Register(p.API.Mux)
	(&metrics.ReportsAPI{S: p.Metrics}).Register(p.API.Mux)
	notes := &alerts.Service{DB: db, Name: func(id int64) string {
		var b core.Bot
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if p.Core.Call(ctx, "bots.get", id, &b) != nil {
			return ""
		}
		return b.Name
	}}
	notes.Register(p.API.Mux)
	secretAsks := &secretreq.Store{DB: db, Notes: notes}
	p.Bots.OnSecretSet = secretAsks.Fulfilled
	(&secretreq.API{Store: secretAsks, WriteError: bots.WriteCoreError,
		Exists: func(ctx context.Context, id int64) error { return p.Core.Call(ctx, "bots.get", id, nil) },
		CreateBot: func(ctx context.Context, name, token string, group int64) (int64, string, error) {
			b, err := p.Bots.CreateBot(ctx, name, token, group)
			return b.ID, b.Username, err
		}}).Register(p.API.Mux)
	p.Go(secretAsks.RunExpiry)
	rules := &alerts.Rules{S: p.Metrics, Notes: notes, Bots: func(ctx context.Context) []int64 {
		var bs []core.Bot
		if p.Core.Call(ctx, "bots.list", struct{}{}, &bs) != nil {
			return nil
		}
		ids := make([]int64, len(bs))
		for i, b := range bs {
			ids[i] = b.ID
		}
		return ids
	}}
	documents := &botconfig.Service{Core: p.Core, Rules: rules, WriteError: bots.WriteCoreError, Group: func(ctx context.Context, id int64) (*botconfig.Group, error) {
		metas, err := p.Bots.Groups.Metas(ctx)
		if err != nil {
			return nil, err
		}
		groupID := metas[id].GroupID
		if groupID == 0 {
			return nil, nil
		}
		groups, err := p.Bots.Groups.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			if g.ID == groupID {
				return &botconfig.Group{ID: g.ID, Name: g.Name}, nil
			}
		}
		return nil, nil
	}}
	documents.Secrets = secretAsks
	rules.Lock = documents.Guard.Lock
	documents.Register(p.API.Mux)
	exportFiles := &backup.Files{Dir: filepath.Join(cfg.DataDir, "export-temp")}
	(&sources.Exports{Core: p.Core, Documents: documents, Metrics: p.Metrics, Extras: exportFiles, Groups: p.Bots.Groups}).Register(p.API.Mux, p.CoreMux)
	(&sources.FullBackups{Core: p.Core, DB: p.DB, W: p.W, PKI: filepath.Join(cfg.DataDir, "pki"), Extras: exportFiles, Mu: &p.backupPartsMu, Blocked: p.restoring.Load}).Register(p.API.Mux, p.CoreMux)
	p.registerRestore()
	p.Go(exportFiles.Run)
	rules.Register(p.API.Mux)
	p.Go(rules.Run)
	p.Events.Subscribe(notes.OnCoreEvent)
	p.Events.Subscribe(documents.OnCoreEvent)
	p.Go(func(ctx context.Context) {
		for {
			notes.Prune(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(6 * time.Hour):
			}
		}
	})
	panicStore := &panics.Store{DB: db}
	panicStore.Register(p.API.Mux)
	(&bots.OverviewAPI{Bots: p.Bots, Metrics: p.Metrics, Panics: panicStore, Rules: rules}).Register(p.API.Mux)
	p.Events.Subscribe(panicStore.OnCoreEvent)
	p.Go(func(ctx context.Context) {
		for {
			panicStore.Prune(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(6 * time.Hour):
			}
		}
	})
	p.Go(func(ctx context.Context) {
		p.Metrics.RunReports(ctx, func(botID int64, data map[string]any) {
			ev := events.Event{Type: "deploy_worse", BotID: botID, Severity: events.Warn, Data: data}
			if err := p.Core.Call(ctx, events.MethodEmit, ev, nil); err != nil {
				log.Warn("deploy report event not sent", "bot", botID, "err", err)
			}
		})
	})
	p.Events.Subscribe(p.Metrics.OnCoreEvent)
	usersAPI := &users.API{Core: p.Core, WriteErr: bots.WriteCoreError}
	usersAPI.Register(p.API.Mux)
	usersAPI.RegisterPayments(p.API.Mux)
	p.Settings = &settings.Store{DB: db}
	p.Settings.Register(p.API.Mux)
	p.Bots.Owner = func(r *http.Request) int64 { return p.Settings.Get(r.Context()).OwnerTgID }
	usersAPI.RegisterClosed(p.API.Mux, func(r *http.Request) int64 { return p.Settings.Get(r.Context()).OwnerTgID })
	serviceBot := &servicebot.Service{DB: db, Core: p.Core, Notes: notes, Log: log, WriteErr: bots.WriteCoreError,
		Owner: func(ctx context.Context) int64 { return p.Settings.Get(ctx).OwnerTgID }}
	serviceBot.Register(p.API.Mux)
	p.Go(serviceBot.Run)
	dbAPI := &dbgate.API{Core: p.Core, WriteErr: bots.WriteCoreError}
	dbAPI.RegisterAPI(p.API.Mux)
	dbAPI.RegisterBackups(p.API.Mux)
	checks := &dbgate.Checks{DB: db, Table: "settings", Run: func(ctx context.Context) []dbgate.Check {
		return []dbgate.Check{dbgate.CheckFile(ctx, "panel.db", db.Path, 0)}
	}, OnBad: func(ch dbgate.Check) { log.Error("panel.db failed quick_check", "error", ch.Error) }}
	p.Go(checks.Loop)
	p.API.Mux.HandleFunc("GET /api/server/checks", func(w http.ResponseWriter, r *http.Request) {
		out := checks.Last(r.Context())
		var core []dbgate.Check
		if p.Core.Call(r.Context(), "db.checks", struct{}{}, &core) == nil {
			out = append(out, core...)
		}
		api.WriteJSON(w, http.StatusOK, out)
	})
	(&metrics.ServerAPI{S: p.Metrics, Procs: func(ctx context.Context) ([]procstat.Stats, error) {
		var ps []procstat.Stats
		err := p.Core.Call(ctx, "procs", struct{}{}, &ps)
		return ps, err
	}}).Register(p.API.Mux)
	hub := &livehub.LiveHub{Core: p.Core, Log: log}
	hub.Register(p.API.Mux)
	p.Go(hub.Run)
	p.Go(func(ctx context.Context) {
		p.Metrics.Run(ctx,
			func(ctx context.Context, after int64) ([]metrics.Minute, error) {
				var ms []metrics.Minute
				err := p.Core.Call(ctx, counters.MethodMinutes, after, &ms)
				return ms, err
			},
			func(ctx context.Context, upTo int64) error {
				return p.Core.Call(ctx, counters.MethodAck, upTo, nil)
			})
	})
	// The core journal (SSH, autorollback, schedule) flows into the audit.
	p.Go(func(ctx context.Context) {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			if c := p.Core.Conn(); c != nil {
				if err := p.Audit.MergeJournal(ctx, c); err != nil && ctx.Err() == nil {
					log.Warn("merging core journal failed", "err", err)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	})
	mcpServer, err := mcp.New(p.Audit.Middleware(p.API.Mux), p.Audit, log)
	if err != nil {
		p.W.Close()
		p.DB.Close()
		return nil, err
	}
	mcpServer.Groups = p.Bots.Groups
	mcpServer.DB = p.DB
	mcpServer.Notes = notes
	mcpServer.Secrets = secretAsks
	p.Go(mcpServer.RunExpiry)
	mcpServer.Register(p.API.Mux)
	p.API.Mux.ServeMux.Handle("/mcp", mcpServer)
	p.API.Use(mcpServer.Direct)
	aidocs.Register(p.API.Mux)
	if err := p.setupWebAccess(notes); err != nil {
		_ = p.W.Close()
		_ = p.DB.Close()
		return nil, err
	}
	return p, nil
}

// setupWebAccess adds the passkey listener (with a domain) and the
// owner's "Access" API (always: it explains that a domain is needed).
func (p *Panel) setupWebAccess(notes *alerts.Service) error {
	st := &webaccess.Store{DB: p.DB}
	web, err := webaccess.New(st, p.Cfg.Domain, p.Cfg.WebListen, p.API, api.Static(p.Cfg.WebDir), p.Log)
	if err != nil {
		return err
	}
	web.Cert = func(ctx context.Context) (*tls.Certificate, error) {
		for _, conn := range p.EdgeSrv.Conns() {
			if conn.Proto() != wire.ProtoEdgePanel {
				continue
			}
			var wc edge.WebCert
			if err := conn.Call(ctx, edge.MethodWebCert, struct{}{}, &wc); err != nil {
				return nil, err
			}
			c, err := tls.X509KeyPair(wc.Chain, wc.Key)
			return &c, err
		}
		return nil, errors.New("the edge is not connected")
	}
	web.Pending = func(ctx context.Context, name, code, ip string) {
		notes.Notify(ctx, 0, alerts.Note{Kind: "new_device", Severity: "warn", Key: "webdev:" + code,
			Text: "новый ключ входа «" + name + "» ждёт подтверждения, код: " + code + " (IP " + ip + ")", Href: "/access"})
	}
	web.Register(p.API.Mux)
	p.Go(func(ctx context.Context) {
		for {
			st.Prune(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Hour):
			}
		}
	})
	if web.Enabled() {
		p.Go(func(ctx context.Context) {
			if err := web.Run(ctx); err != nil {
				p.Log.Error("passkey listener stopped", "err", err)
			}
		})
	}
	return nil
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
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	clientCtx, stopClient := context.WithCancel(parent)
	defer stopClient()
	var listeners sync.WaitGroup
	ln, err := wire.Listen(wire.PanelSock(p.Cfg.RunDir), 0o660, p.Cfg.SocketGID)
	if err != nil {
		return fmt.Errorf("panel socket: %w", err)
	}
	safe.Go(p.Log, "panel socket", func() { p.EdgeSrv.Serve(ln) })
	safe.Go(p.Log, "core client", func() { p.Core.Run(clientCtx) })
	safe.Go(p.Log, "stats", func() { app.ReportStats(ctx, p.Core, "panel", 10*time.Second) })
	listeners.Add(1)
	safe.Go(p.Log, "mtls listener", func() {
		defer listeners.Done()
		if err := p.API.ListenAndServe(ctx, p.Cfg.Listen, p.Cfg.PublicAddr); err != nil {
			p.Log.Error("mtls listener stopped", "err", err)
		}
	})
	if p.Cfg.Dev {
		listeners.Add(1)
		safe.Go(p.Log, "dev listener", func() {
			defer listeners.Done()
			if err := p.API.ListenDev(ctx, p.Cfg.DevAddr); err != nil {
				p.Log.Error("dev listener stopped", "err", err)
			}
		})
	}
	for _, fn := range p.runners {
		safe.Go(p.Log, "panel task", func() { fn(ctx) })
	}
	p.Log.Info("panel started", "version", buildinfo.Version)
	var restarting *app.RestartRequest
	select {
	case <-ctx.Done():
	case request := <-p.restart:
		restarting = &request
	}
	cancel()
	p.Log.Info("panel stopping")
	ln.Close()
	for _, conn := range p.EdgeSrv.Conns() {
		conn.Close()
	}
	listeners.Wait()
	if restarting != nil {
		p.backupPartsMu.Lock()
		defer p.backupPartsMu.Unlock()
	}
	steps := make([]func(context.Context) error, 0, len(p.closers)+1)
	for i := len(p.closers) - 1; i >= 0; i-- {
		steps = append(steps, p.closers[i])
	}
	steps = append(steps, func(context.Context) error { p.W.Close(); return p.DB.Close() })
	app.Shutdown(p.Log, 15*time.Second, steps...)
	if restarting != nil {
		err := restarting.Apply()
		if restarting.Done != nil {
			restarting.Done <- err
		}
		if restarting.Replied != nil {
			select {
			case <-restarting.Replied:
			case <-parent.Done():
			case <-time.After(5 * time.Second):
			}
		}
		if err != nil {
			p.Log.Error("panel restore failed", "err", err)
		}
		return app.ErrRestart
	}
	return nil
}
