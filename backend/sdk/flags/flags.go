// Package flags supplies typed runtime settings and autonomous env defaults.
package flags

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const MaxFlags = 100

var flagName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

type Value struct {
	Kind string `cbor:"1,keyasint"`
	Bool bool   `cbor:"2,keyasint,omitempty"`
	Int  int64  `cbor:"3,keyasint,omitempty"`
	Text string `cbor:"4,keyasint,omitempty"`
}

func (v Value) MarshalJSON() ([]byte, error) {
	switch v.Kind {
	case "bool":
		return json.Marshal(v.Bool)
	case "int":
		return json.Marshal(v.Int)
	case "string":
		return json.Marshal(v.Text)
	case "duration":
		return json.Marshal(time.Duration(v.Int).String())
	}
	return nil, fmt.Errorf("sdk: unknown flag type")
}
func (v *Value) UnmarshalJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var raw any
	if err := d.Decode(&raw); err != nil {
		return err
	}
	switch raw := raw.(type) {
	case bool:
		*v = Value{Kind: "bool", Bool: raw}
	case json.Number:
		n, err := raw.Int64()
		if err != nil {
			return err
		}
		*v = Value{Kind: "int", Int: n}
	case string:
		*v = Value{Kind: "string", Text: raw}
	default:
		return fmt.Errorf("sdk: flag value must be bool, integer or string")
	}
	return nil
}
func Normalize(kind string, v Value) (Value, error) {
	if kind == "duration" && v.Kind == "string" {
		d, err := time.ParseDuration(v.Text)
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: "duration", Int: int64(d)}, nil
	}
	if kind != v.Kind {
		return Value{}, fmt.Errorf("sdk: flag value must have type %s", kind)
	}
	switch kind {
	case "bool":
		return Value{Kind: kind, Bool: v.Bool}, nil
	case "int", "duration":
		return Value{Kind: kind, Int: v.Int}, nil
	case "string":
		if len(v.Text) > 4096 {
			return Value{}, fmt.Errorf("sdk: flag string is too long")
		}
		return Value{Kind: kind, Text: v.Text}, nil
	}
	return Value{}, fmt.Errorf("sdk: invalid flag type")
}

type Definition struct {
	Name        string `cbor:"1,keyasint" json:"name"`
	Kind        string `cbor:"2,keyasint" json:"kind"`
	Default     Value  `cbor:"3,keyasint" json:"default"`
	Description string `cbor:"4,keyasint" json:"description"`
}

func Validate(d Definition) error {
	if !flagName.MatchString(d.Name) || len(d.Description) > 1024 {
		return fmt.Errorf("sdk: invalid flag definition")
	}
	_, err := Normalize(d.Kind, d.Default)
	return err
}

type State struct {
	Value   Value `cbor:"1,keyasint"`
	Version int64 `cbor:"2,keyasint"`
	Reset   bool  `cbor:"3,keyasint,omitempty"`
}
type Caller interface {
	Call(context.Context, string, any, any) error
}
type Client interface {
	Caller
	Connected() bool
	Use(string) error
}
type entry struct {
	def      Definition
	fallback Value
	value    atomic.Value
	version  int64
}
type Registry struct {
	mu         sync.Mutex
	registerMu sync.Mutex
	entries    map[string]*entry
	client     Client
	log        *slog.Logger
	epoch      uint64
}

func New(client Client, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.Default()
	}
	return &Registry{client: client, log: log, entries: map[string]*entry{}}
}

func envDefault(d Definition) Value {
	raw, ok := os.LookupEnv("TGPANEL_FLAG_" + strings.ToUpper(d.Name))
	if !ok {
		return d.Default
	}
	v := Value{Kind: d.Kind}
	var err error
	switch d.Kind {
	case "bool":
		v.Bool, err = strconv.ParseBool(raw)
	case "int":
		v.Int, err = strconv.ParseInt(raw, 10, 64)
	case "string":
		v.Text = raw
	case "duration":
		var duration time.Duration
		duration, err = time.ParseDuration(raw)
		v.Int = int64(duration)
	}
	if err != nil {
		return d.Default
	}
	v, err = Normalize(d.Kind, v)
	if err != nil {
		return d.Default
	}
	return v
}

func (r *Registry) add(d Definition) *entry {
	fallback := envDefault(d)
	r.mu.Lock()
	if existing := r.entries[d.Name]; existing != nil && existing.def.Kind == d.Kind {
		r.mu.Unlock()
		return existing
	}
	e := &entry{def: d, fallback: fallback}
	e.value.Store(fallback)
	if err := Validate(d); err != nil || len(r.entries) >= MaxFlags || r.entries[d.Name] != nil {
		r.mu.Unlock()
		r.log.Warn("sdk: flag ignored, invalid definition or flag limit", "flag", d.Name)
		return e
	}
	r.entries[d.Name] = e
	r.mu.Unlock()
	if err := r.client.Use("flags"); err != nil {
		r.log.Warn("sdk: flag module registration failed", "err", err)
	}
	if r.client.Connected() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.Sync(ctx, r.client); err != nil {
			r.log.Warn("sdk: flag values unavailable", "err", err)
		}
	}
	return e
}

func (r *Registry) Sync(ctx context.Context, c Caller) error {
	r.registerMu.Lock()
	defer r.registerMu.Unlock()
	r.mu.Lock()
	epoch := r.epoch
	defs := make([]Definition, 0, len(r.entries))
	for _, e := range r.entries {
		defs = append(defs, e.def)
	}
	r.mu.Unlock()
	if len(defs) == 0 {
		return nil
	}
	var values map[string]State
	if err := c.Call(ctx, "sdk.flags.register", defs, &values); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if epoch == r.epoch {
		r.apply(values)
	}
	return nil
}
func (r *Registry) Connected(ctx context.Context, c Caller) error {
	r.mu.Lock()
	r.epoch++
	for _, e := range r.entries {
		e.version = 0
	}
	r.mu.Unlock()
	return r.Sync(ctx, c)
}
func (r *Registry) Disconnected() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.epoch++
	for _, e := range r.entries {
		e.version = 0
		e.value.Store(e.fallback)
	}
}
func (r *Registry) Update(values map[string]State) { r.mu.Lock(); defer r.mu.Unlock(); r.apply(values) }
func (r *Registry) apply(values map[string]State) {
	for name, state := range values {
		e := r.entries[name]
		if e == nil || state.Version < e.version {
			continue
		}
		v := state.Value
		if state.Reset {
			v = e.def.Default
		}
		v, err := Normalize(e.def.Kind, v)
		if err == nil {
			e.value.Store(v)
			e.version = state.Version
		}
	}
}

type Bool struct{ entry *entry }

func (v *Bool) On() bool { return v.entry.value.Load().(Value).Bool }

type Int struct{ entry *entry }

func (v *Int) Value() int64 { return v.entry.value.Load().(Value).Int }

type String struct{ entry *entry }

func (v *String) Value() string { return v.entry.value.Load().(Value).Text }

type Duration struct{ entry *entry }

func (v *Duration) Value() time.Duration { return time.Duration(v.entry.value.Load().(Value).Int) }
func (r *Registry) Bool(name string, value bool, description string) *Bool {
	return &Bool{r.add(Definition{Name: name, Kind: "bool", Default: Value{Kind: "bool", Bool: value}, Description: description})}
}
func (r *Registry) Int(name string, value int64, description string) *Int {
	return &Int{r.add(Definition{Name: name, Kind: "int", Default: Value{Kind: "int", Int: value}, Description: description})}
}
func (r *Registry) String(name, value, description string) *String {
	return &String{r.add(Definition{Name: name, Kind: "string", Default: Value{Kind: "string", Text: value}, Description: description})}
}
func (r *Registry) Duration(name string, value time.Duration, description string) *Duration {
	return &Duration{r.add(Definition{Name: name, Kind: "duration", Default: Value{Kind: "duration", Int: int64(value)}, Description: description})}
}
