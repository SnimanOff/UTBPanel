// Package sdkhost serves a private SDK socket for each bot instance.
// It deliberately does not expose the core/panel method router to bots.
package sdkhost

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk"
	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
)

type Info struct {
	Version   string   `cbor:"1,keyasint" json:"version"`
	Modules   []string `cbor:"2,keyasint" json:"modules"`
	Connected bool     `cbor:"3,keyasint" json:"connected"`
}

type key struct {
	id       int64
	instance string
}
type endpoint struct {
	ln    net.Listener
	srv   *wire.Server
	conn  *wire.Conn
	info  Info
	ready bool
}

type Host struct {
	Dir string
	Log *slog.Logger
	// Setup attaches per-bot modules to this private router and returns an
	// optional transport-disconnect hook for their resources.
	Setup     func(int64, string, *wire.Mux) func(*wire.Conn)
	mu        sync.Mutex
	endpoints map[key]*endpoint
}

func (h *Host) Path(id int64, instance string) string {
	name := strconv.FormatInt(id, 10)
	if instance == "canary" {
		name += "-canary"
	}
	return filepath.Join(h.Dir, name+".sock")
}

// Start creates the listener before the bot process starts. A new process
// receives a fresh session, so readiness cannot leak across restarts.
// uid < 0 is only for platforms without Unix credentials (development).
func (h *Host) Start(id int64, instance string, uid int) error {
	if id <= 0 || (instance != "" && instance != "canary") {
		return fmt.Errorf("sdk: invalid bot instance")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := os.MkdirAll(h.Dir, 0o711); err != nil {
		return err
	}
	if err := os.Chmod(h.Dir, 0o711); err != nil {
		return err
	}
	k := key{id, instance}
	if old := h.endpoints[k]; old != nil {
		closeEndpoint(old)
		delete(h.endpoints, k)
	}
	ln, err := wire.Listen(h.Path(id, instance), 0o600, -1)
	if err != nil {
		return err
	}
	if uid >= 0 {
		if err := os.Chown(h.Path(id, instance), uid, -1); err != nil {
			ln.Close()
			return err
		}
	}
	e := &endpoint{ln: ln}
	mux := wire.NewMux()
	var disconnected func(*wire.Conn)
	if h.Setup != nil {
		disconnected = h.Setup(id, instance, mux)
	}
	mux.Handle("sdk.register", wire.Func(func(_ context.Context, c *wire.Conn, r sdk.Registration) (any, error) {
		if r.Version == "" || len(r.Version) > 64 || r.Instance != instance || len(r.Modules) > 16 {
			return nil, wire.Errorf(wire.CodeBadRequest, "invalid SDK registration")
		}
		for _, m := range r.Modules {
			if !slices.Contains([]string{"db", "ready", "metrics", "tasks", "flags"}, m) {
				return nil, wire.Errorf(wire.CodeBadRequest, "unsupported SDK module %q", m)
			}
		}
		modules := slices.Clone(r.Modules)
		slices.Sort(modules)
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.endpoints[k] != e {
			return nil, wire.ErrClosed
		}
		// One live session per process instance. A second connection may
		// not replace a session or inherit its state.
		if e.conn != nil && e.conn != c {
			return nil, wire.Errorf("conflict", "SDK already connected")
		}
		e.conn = c
		e.info = Info{Version: r.Version, Modules: slices.Compact(modules), Connected: true}
		return nil, nil
	}))
	mux.Handle("sdk.ready", func(_ context.Context, c *wire.Conn, _ wire.Raw) (any, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.endpoints[k] != e || e.conn != c || !slices.Contains(e.info.Modules, "ready") {
			return nil, wire.Errorf(wire.CodeBadRequest, "ready module is not registered")
		}
		e.ready = true
		return nil, nil
	})
	e.srv = &wire.Server{Mux: mux, Log: h.Log,
		Hello:            wire.Hello{Proto: sdk.Protocol, Version: sdk.ProtocolVersion, MinVersion: max(1, sdk.ProtocolVersion-2), Name: "core-sdk", Caps: []string{"db", "ready", "metrics", "tasks", "flags"}},
		HandshakeTimeout: 5 * time.Second,
		Accept:           func(c net.Conn) error { return checkPeer(c, uid) },
		OnDisconnect: func(c *wire.Conn, _ error) {
			if disconnected != nil {
				disconnected(c)
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if e.conn == c {
				e.conn = nil
				e.info.Connected = false
				e.ready = false
			}
		},
	}
	if h.endpoints == nil {
		h.endpoints = map[key]*endpoint{}
	}
	h.endpoints[k] = e
	go func() {
		if err := e.srv.Serve(ln); err != nil && h.Log != nil {
			h.Log.Warn("SDK listener stopped", "bot", id, "err", err)
		}
	}()
	return nil
}

// Readiness keeps the module requirement through a disconnect. A reconnect
// must replay Ready before this instance can finish starting.
func (h *Host) Readiness(id int64, instance string) (usesSDK, ready bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.endpoints[key{id, instance}]
	if e == nil {
		return false, false
	}
	return slices.Contains(e.info.Modules, "ready"), e.info.Connected && e.ready
}

func (h *Host) Info(id int64, instance string) *Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.endpoints[key{id, instance}]
	if e == nil || e.info.Version == "" {
		return nil
	}
	i := e.info
	i.Modules = slices.Clone(i.Modules)
	return &i
}

// Call addresses one registered process instance. It never switches a
// running request to a replacement transport session.
func (h *Host) Call(ctx context.Context, id int64, instance, method string, in, out any) error {
	h.mu.Lock()
	e := h.endpoints[key{id, instance}]
	var c *wire.Conn
	if e != nil {
		c = e.conn
	}
	h.mu.Unlock()
	if c == nil {
		return wire.ErrNotConnected
	}
	return c.Call(ctx, method, in, out)
}

func closeEndpoint(e *endpoint) {
	e.ln.Close()
	for _, c := range e.srv.Conns() {
		c.Close()
	}
}

func (h *Host) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, e := range h.endpoints {
		closeEndpoint(e)
		delete(h.endpoints, k)
	}
	return nil
}
