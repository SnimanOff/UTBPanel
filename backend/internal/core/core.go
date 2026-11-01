// Package core wires the core process: core.db, the core socket and the
// services that run bots.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/buildinfo"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/counters"
	"github.com/SnimanOff/UTBPanel/backend/internal/dbgate"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/logs"
	"github.com/SnimanOff/UTBPanel/backend/internal/procstat"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/safe"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
	"github.com/SnimanOff/UTBPanel/backend/internal/users"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Protocol versions served by the core.
const (
	EdgeVersion  = 1
	PanelVersion = 1
)

// Core is the running core process.
type Core struct {
	Cfg       config.Core
	Log       *slog.Logger
	DB        *store.DB
	W         *store.Writer
	Mux       *wire.Mux
	Srv       *wire.Server
	Ev        *events.Bus
	Sec       *secrets.Store
	TG        *telegram.Client
	Runner    *runner.Runner
	Logs      *logs.Store
	Journal   *journal.Journal
	Releases  *deploy.Store
	Pipe      *ingress.Pipeline
	Users     *users.Tracker
	Count     *counters.Counters
	Host      *counters.HostSampler
	Live      *counters.Live
	DBG       *dbgate.Browser
	DBBackups *dbgate.Backups
	DBChecks  *dbgate.Checks
	hostMu    sync.Mutex
	hostLast  counters.Server

	mu           sync.Mutex
	procs        map[string]procstat.Stats
	edge         edgeSync
	targets      targets
	delivery     delivery
	cpuMu        sync.Mutex
	cpuPrev      map[int64]cpuSample
	cpuLast      map[int64]float64
	onStarted    []func(context.Context, runner.Bot)
	onStopping   []func(context.Context, runner.Bot)
	pausedChecks []func(context.Context, int64) bool

	// closers run in reverse order on shutdown.
	closers []func(context.Context) error
}

// New opens core.db and prepares the socket server.
func New(ctx context.Context, cfg config.Core, log *slog.Logger) (*Core, error) {
	for _, d := range []string{cfg.DataDir, cfg.RunDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	key, err := secrets.LoadMasterKey(cfg.KeysDir)
	if err != nil {
		return nil, err
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		return nil, err
	}
	// Decrypted secrets never reach the logs.
	redact := secrets.NewRedactor()
	log = slog.New(redact.Handler(log.Handler()))
	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, "core.db"), migrations.Core())
	if err != nil {
		return nil, err
	}
	c := &Core{
		Cfg: cfg, Log: log, DB: db,
		W:       store.NewWriter(db, 10*time.Second, log),
		Mux:     wire.NewMux(),
		procs:   map[string]procstat.Stats{},
		cpuPrev: map[int64]cpuSample{},
		cpuLast: map[int64]float64{},
		Sec:     &secrets.Store{DB: db, Box: box, Redact: redact},
		TG:      telegram.New(),
	}
	c.TG.Base = cfg.TelegramAPI
	c.Srv = &wire.Server{
		Mux:   c.Mux,
		Hello: app.Hello(wire.ProtoCorePanel, "core", PanelVersion, 1),
		Also: []wire.Hello{
			app.Hello(wire.ProtoEdgeCore, "core", EdgeVersion, 1),
			app.Hello(wire.ProtoCoreCLI, "core", 1, 1),
		},
		Log: log,
		OnConnect: func(conn *wire.Conn) {
			log.Info("peer connected", "proto", conn.Proto(), "peer", conn.Peer().Hello.Name, "version", conn.Version())
		},
		OnDisconnect: func(conn *wire.Conn, err error) {
			log.Info("peer disconnected", "proto", conn.Proto(), "err", err)
		},
	}
	c.Ev = events.NewBus(db, c.Srv, c.Mux, log)
	c.Journal = journal.New(db, c.Mux, log)
	c.registerSecrets()
	c.registerBots()
	c.Count = counters.New()
	c.Count.Register(c.Mux)
	c.Host = &counters.HostSampler{Proc: counters.Root(cfg.HostProc), DataDir: cfg.DataDir}
	c.Logs = c.newLogs()
	c.registerLogs()
	c.Runner = c.newRunner()
	c.registerLifecycle()
	c.Releases = c.newReleases()
	c.registerReleases()
	c.registerSettings()
	c.registerEdgeSync()
	c.Pipe = c.newPipeline()
	c.registerIngress()
	c.registerDeferred()
	c.registerWebhooks()
	c.Users = &users.Tracker{DB: db, W: c.W, Log: log, OnNew: c.Count.NewUser, OnBlock: c.Count.Block, OnPayment: c.Count.Payment}
	c.Pipe.Observe = append(c.Pipe.Observe, c.Count.Observe)
	c.Users.Register(c.Mux)
	c.DBG = &dbgate.Browser{Path: func(id int64) string {
		return filepath.Join(c.Cfg.DataDir, "bots", strconv.FormatInt(id, 10), "bot.db")
	}}
	c.DBG.Register(c.Mux, c.Journal)
	c.DBBackups = c.newBackups()
	c.DBChecks = c.newChecks()
	c.registerDBBackups()
	c.Live = &counters.Live{
		InFlight: c.Pipe.InFlight,
		Usage: func(id int64) (float64, int64) {
			u, _ := c.Runner.Usage(id)
			c.cpuMu.Lock()
			cpu := c.cpuLast[id]
			c.cpuMu.Unlock()
			return cpu, u.RSS
		},
		Host: c.HostNow,
		Bots: c.runningBots,
	}
	c.Live.Register(c.Mux)
	c.Pipe.Observe = append(c.Pipe.Observe, c.Live.Observe)
	c.Pipe.Account = append(c.Pipe.Account, c.Users.Account, c.Users.Payments)
	c.Mux.Handle("ping", func(context.Context, *wire.Conn, wire.Raw) (any, error) {
		return buildinfo.String("core"), nil
	})
	c.Mux.OnEvent(app.EventProcStats, func(_ *wire.Conn, _ string, body wire.Raw) {
		var s procstat.Stats
		if wire.Unmarshal(body, &s) == nil {
			c.setProc(s)
		}
	})
	c.Mux.Handle("procs", func(context.Context, *wire.Conn, wire.Raw) (any, error) {
		return c.Procs(), nil
	})
	return c, nil
}

func (c *Core) setProc(s procstat.Stats) {
	c.mu.Lock()
	c.procs[s.Name] = s
	c.mu.Unlock()
}

// Procs returns the latest stats of edge, core and panel.
func (c *Core) Procs() []procstat.Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]procstat.Stats, 0, len(c.procs))
	for _, s := range c.procs {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// OnClose registers a shutdown step.
func (c *Core) OnClose(fn func(context.Context) error) { c.closers = append(c.closers, fn) }

// Run serves the core socket until ctx is done, then stops gracefully.
func (c *Core) Run(ctx context.Context) error {
	ln, err := wire.Listen(wire.CoreSock(c.Cfg.RunDir), 0o660, c.Cfg.SocketGID)
	if err != nil {
		return fmt.Errorf("core socket: %w", err)
	}
	safe.Go(c.Log, "core socket", func() { c.Srv.Serve(ln) })
	safe.Go(c.Log, "events", func() { c.Ev.Run(ctx) })
	safe.Go(c.Log, "restore bots", func() { c.Runner.Restore(ctx) })
	safe.Go(c.Log, "limits watcher", func() { c.Runner.Watch(ctx) })
	safe.Go(c.Log, "webhook checks", func() { c.watchWebhooks(ctx) })
	safe.Go(c.Log, "users", func() { c.Users.Run(ctx) })
	safe.Go(c.Log, "server metrics", func() { c.sampleHost(ctx) })
	safe.Go(c.Log, "live metrics", func() { c.Live.Run(ctx) })
	safe.Go(c.Log, "db backups", func() { c.dailyBackups(ctx) })
	safe.Go(c.Log, "db checks", func() { c.DBChecks.Loop(ctx) })
	safe.Go(c.Log, "self stats", func() {
		var s procstat.Sampler
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			c.setProc(s.Sample("core"))
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	})
	c.Log.Info("core started", "version", buildinfo.Version, "socket", wire.CoreSock(c.Cfg.RunDir))
	<-ctx.Done()
	c.Log.Info("core stopping")
	ln.Close()
	steps := make([]func(context.Context) error, 0, len(c.closers)+2)
	for i := len(c.closers) - 1; i >= 0; i-- {
		steps = append(steps, c.closers[i])
	}
	steps = append(steps,
		// Bots stop, their saved state and webhooks stay: they come back
		// when the core starts again.
		func(context.Context) error { c.Runner.Shutdown(); c.Logs.Close(); return nil },
		func(context.Context) error {
			for _, conn := range c.Srv.Conns() {
				conn.Close()
			}
			return nil
		},
		func(context.Context) error { c.W.Close(); return c.DB.Close() },
	)
	app.Shutdown(c.Log, 30*time.Second, steps...)
	return nil
}

// sampleHost reads the host every second: live metrics use the last sample,
// history gets the maximum of each minute.
func (c *Core) sampleHost(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s := c.Host.Sample()
		c.hostMu.Lock()
		c.hostLast = s
		c.hostMu.Unlock()
		c.Count.RecordServer(s)
		if time.Now().Second() == 0 {
			for _, id := range c.runningBots() {
				db, wal := c.DBG.Sizes(id)
				c.Count.Gauge(id, "db_size", float64(db))
				c.Count.Gauge(id, "db_wal", float64(wal))
			}
		}
		for _, p := range c.Procs() {
			c.Count.Gauge(counters.ServerBotID, "proc_"+p.Name+"_cpu", p.CPUPercent)
			c.Count.Gauge(counters.ServerBotID, "proc_"+p.Name+"_rss", float64(p.RSS))
		}
	}
}

// HostNow returns the last host sample.
func (c *Core) HostNow() counters.Server {
	c.hostMu.Lock()
	defer c.hostMu.Unlock()
	return c.hostLast
}
