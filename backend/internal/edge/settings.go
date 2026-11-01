package edge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// BotSettings is what the edge knows about one bot. It holds no token: a
// webhook secret only lets someone send a fake update, and only from
// Telegram's addresses.
type BotSettings struct {
	Secret string `cbor:"1,keyasint" json:"secret"`
	// Maintenance is on for scheduled maintenance and manual pause alike.
	Maintenance bool `cbor:"2,keyasint" json:"maintenance,omitempty"`
	// MaintenanceText is already rendered with {start} and {end}.
	MaintenanceText string `cbor:"3,keyasint" json:"maintenance_text,omitempty"`
	// UnavailableText is sent while the core is down and nothing is planned.
	UnavailableText string `cbor:"4,keyasint" json:"unavailable_text,omitempty"`
	TZ              string `cbor:"5,keyasint" json:"tz,omitempty"`
	// MaintenanceTemplate with StartsAt and EndsAt is rendered at answer
	// time in TZ; MaintenanceText is the fallback.
	MaintenanceTemplate string    `cbor:"6,keyasint" json:"maintenance_template,omitempty"`
	StartsAt            time.Time `cbor:"7,keyasint" json:"starts_at,omitempty"`
	EndsAt              time.Time `cbor:"8,keyasint" json:"ends_at,omitempty"`
}

// Settings is the whole document pushed by the core.
type Settings struct {
	Version int64                 `cbor:"1,keyasint" json:"version"`
	Bots    map[int64]BotSettings `cbor:"2,keyasint" json:"bots"`
}

// Socket methods between edge and core.
const (
	MethodSettings = "edge.settings" // core → edge
	MethodCert     = "edge.cert"     // edge → core: PEM of the self-signed certificate
)

// settings is the edge's copy, also kept in /data/edge/settings so the
// edge works by the last known rules while the core is down.
type settings struct {
	mu   sync.RWMutex
	path string
	doc  Settings
}

func (s *settings) secret(id int64) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.doc.Bots[id]
	return b.Secret, ok
}

func (s *settings) bot(id int64) (BotSettings, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.doc.Bots[id]
	return b, ok
}

func (s *settings) version() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doc.Version
}

// setSecrets is a shortcut for tests.
func (s *settings) setSecrets(m map[int64]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc.Bots = map[int64]BotSettings{}
	for id, sec := range m {
		s.doc.Bots[id] = BotSettings{Secret: sec}
	}
}

// load reads the saved file.
func (s *settings) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var d Settings
	if err := json.Unmarshal(b, &d); err != nil {
		return err
	}
	s.mu.Lock()
	s.doc = d
	s.mu.Unlock()
	return nil
}

// apply replaces the settings and saves them atomically (0600).
func (s *settings) apply(d Settings) error {
	if d.Bots == nil {
		d.Bots = map[int64]BotSettings{}
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.mu.Lock()
	s.doc = d
	s.mu.Unlock()
	return nil
}

// registerSettings lets the core push settings; the edge answers with the
// version it now has.
func (e *Edge) registerSettings() {
	e.Mux.Handle(MethodSettings, wire.Func(func(_ context.Context, _ *wire.Conn, d Settings) (int64, error) {
		if err := e.settings.apply(d); err != nil {
			e.Log.Error("saving edge settings failed", "err", err)
			return 0, err
		}
		e.Log.Info("settings from core", "version", d.Version, "bots", len(d.Bots))
		return d.Version, nil
	}))
}

// onCoreConnect sends the self-signed certificate (the core passes it to
// setWebhook) and the version of settings the edge has.
func (e *Edge) onCoreConnect(c *wire.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*e.botTimeout()/60)
	defer cancel()
	if err := c.Call(ctx, MethodCert, e.certPEM, nil); err != nil {
		e.Log.Warn("sending certificate to core failed", "err", err)
	}
}
