// Package sdk connects a bot to UTBPanel. Without TGPANEL_SDK it starts
// immediately in autonomous mode. Import sdk/db only if SQLite is needed.
package sdk

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
)

const (
	Version         = "1.0.0"
	Protocol        = "sdk-core"
	ProtocolVersion = 1
)

// Registration is sent again after reconnecting; modules are additive.
type Registration struct {
	Version  string   `cbor:"1,keyasint" json:"version"`
	Modules  []string `cbor:"2,keyasint" json:"modules"`
	Instance string   `cbor:"3,keyasint,omitempty" json:"instance,omitempty"`
}

type Option func(*Kit)

func WithLogger(log *slog.Logger) Option {
	return func(k *Kit) {
		if log != nil {
			k.log = log
		}
	}
}

// WithModules announces modules before the first connection is made.
func WithModules(modules ...string) Option {
	return func(k *Kit) {
		for _, m := range modules {
			k.modules[m] = true
		}
	}
}

// Kit owns one reconnecting connection. Its methods are safe for concurrent use.
type Kit struct {
	mu         sync.Mutex
	registerMu sync.Mutex
	modules    map[string]bool
	conn       *wire.Conn
	changed    chan struct{}
	client     *wire.Client
	mux        *wire.Mux
	log        *slog.Logger
	path       string
	instance   string
	cancel     context.CancelFunc
	done       chan struct{}
}

// New never waits for a panel. Invalid socket settings are logged and leave
// the kit autonomous. Close releases the background reconnect loop.
func New(options ...Option) *Kit {
	k := &Kit{modules: map[string]bool{}, changed: make(chan struct{}), done: make(chan struct{}), mux: wire.NewMux(), log: slog.Default()}
	for _, opt := range options {
		opt(k)
	}
	if os.Getenv("TGPANEL_CANARY") == "1" {
		k.instance = "canary"
	}
	address := os.Getenv("TGPANEL_SDK")
	if address == "" {
		close(k.done)
		return k
	}
	if !strings.HasPrefix(address, "unix://") || !filepath.IsAbs(strings.TrimPrefix(address, "unix://")) {
		k.log.Warn("sdk: invalid TGPANEL_SDK, using autonomous mode")
		close(k.done)
		return k
	}
	k.path = strings.TrimPrefix(address, "unix://")
	ctx, cancel := context.WithCancel(context.Background())
	k.cancel = cancel
	k.client = &wire.Client{Path: k.path, Mux: k.mux, Hello: wire.Hello{Proto: Protocol, Version: ProtocolVersion, MinVersion: 1, Name: "sdk/" + Version, Caps: k.registration().Modules}, Log: k.log}
	k.client.OnConnect = func(c *wire.Conn) {
		if err := k.register(c); err != nil {
			k.log.Warn("sdk: registration failed, using autonomous mode", "err", err)
			c.Close()
			return
		}
		k.mu.Lock()
		k.conn = c
		k.signal()
		k.mu.Unlock()
	}
	k.client.OnDisconnect = func(c *wire.Conn, err error) {
		k.mu.Lock()
		if k.conn == c {
			k.conn = nil
			k.signal()
		}
		k.mu.Unlock()
		if ctx.Err() == nil {
			k.log.Warn("sdk: connection lost, using autonomous mode", "err", err)
		}
	}
	go func() { defer close(k.done); k.client.Run(ctx) }()
	return k
}

func (k *Kit) signal() { close(k.changed); k.changed = make(chan struct{}) }

func (k *Kit) registration() Registration {
	k.mu.Lock()
	defer k.mu.Unlock()
	modules := make([]string, 0, len(k.modules))
	for m := range k.modules {
		modules = append(modules, m)
	}
	slices.Sort(modules)
	return Registration{Version: Version, Modules: modules, Instance: k.instance}
}

func (k *Kit) register(c *wire.Conn) error {
	k.registerMu.Lock()
	defer k.registerMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Call(ctx, "sdk.register", k.registration(), nil)
}

// Use announces a module loaded after New, for example sdk/db.
func (k *Kit) Use(module string) error {
	k.mu.Lock()
	k.modules[module] = true
	c := k.conn
	k.mu.Unlock()
	if c == nil && k.client != nil {
		// OnConnect may still be registering. Serialize a fresh registration
		// so a module added during that handshake is not lost.
		c = k.client.Conn()
	}
	if c != nil {
		return k.register(c)
	}
	return nil
}

// Configured distinguishes an unavailable panel DB from a local SQLite DB.
// A lost connection must never silently switch sdk/db to a local file.
func (k *Kit) Configured() bool { return k.path != "" }

func (k *Kit) Connected() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.conn != nil
}

// Wait waits for registration, not merely for an open socket.
func (k *Kit) Wait(ctx context.Context) error {
	if !k.Configured() {
		return nil
	}
	for {
		k.mu.Lock()
		c, changed := k.conn, k.changed
		k.mu.Unlock()
		if c != nil {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		case <-k.done:
			return wire.ErrClosed
		}
	}
}

// Call returns ErrNotConnected while autonomous; modules decide how to fall back.
func (k *Kit) Call(ctx context.Context, method string, in, out any) error {
	k.mu.Lock()
	c := k.conn
	k.mu.Unlock()
	if c == nil {
		return wire.ErrNotConnected
	}
	return c.Call(ctx, method, in, out)
}

// Handle registers a core-to-bot method (for example a scheduled task).
func (k *Kit) Handle(method string, handler wire.Handler) { k.mux.Handle(method, handler) }

func (k *Kit) Close() error {
	if k.cancel != nil {
		k.cancel()
	}
	<-k.done
	return nil
}

var ErrNotConnected = wire.ErrNotConnected

// IsUnavailable lets optional modules recognize disconnection errors.
func IsUnavailable(err error) bool {
	return errors.Is(err, wire.ErrNotConnected) || errors.Is(err, wire.ErrClosed)
}
