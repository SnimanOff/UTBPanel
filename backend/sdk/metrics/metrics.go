// Package metrics batches optional bot counters and gauges for the core.
package metrics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"math"
	"regexp"
	"sync"
	"time"
)

const MaxNames = 200

var validName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]{0,63}$`)

func ValidName(name string) bool { return validName.MatchString(name) }

type Point struct {
	Name  string  `cbor:"1,keyasint"`
	Kind  string  `cbor:"2,keyasint"`
	Delta int64   `cbor:"3,keyasint,omitempty"`
	Value float64 `cbor:"4,keyasint,omitempty"`
}
type Batch struct {
	Source   string  `cbor:"1,keyasint"`
	Sequence int64   `cbor:"2,keyasint"`
	Points   []Point `cbor:"3,keyasint"`
	Dropped  int64   `cbor:"4,keyasint,omitempty"`
}
type Client interface {
	Connected() bool
	Use(string) error
	Call(context.Context, string, any, any) error
}
type entry struct {
	point Point
	dirty bool
}
type Registry struct {
	client   Client
	mu       sync.Mutex
	flushMu  sync.Mutex
	names    map[string]*entry
	source   string
	sequence int64
	dropped  int64
	pending  *Batch
}

func New(client Client) *Registry {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return &Registry{client: client, names: map[string]*entry{}, source: hex.EncodeToString(id[:])}
}

func (r *Registry) metric(name, kind string) *entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.names[name]; p != nil {
		if p.point.Kind == kind {
			return p
		}
		r.dropped++
		return nil
	}
	if !ValidName(name) || len(r.names) >= MaxNames {
		r.dropped++
		return nil
	}
	p := &entry{point: Point{Name: name, Kind: kind}, dirty: true}
	r.names[name] = p
	return p
}

type Counter struct {
	registry *Registry
	entry    *entry
}
type Value struct {
	registry *Registry
	entry    *entry
}

func (r *Registry) Counter(name string) *Counter {
	r.client.Use("metrics")
	return &Counter{r, r.metric(name, "counter")}
}
func (r *Registry) Value(name string) *Value {
	r.client.Use("metrics")
	return &Value{r, r.metric(name, "value")}
}
func (c *Counter) Add(n int64) {
	if c.entry == nil || n < 0 || !c.registry.client.Connected() {
		return
	}
	r := c.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > math.MaxInt64-c.entry.point.Delta {
		r.dropped++
		return
	}
	c.entry.point.Delta += n
	c.entry.dirty = true
}
func (v *Value) Set(n float64) {
	if v.entry == nil || math.IsNaN(n) || math.IsInf(n, 0) || !v.registry.client.Connected() {
		return
	}
	r := v.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	v.entry.point.Value = n
	v.entry.dirty = true
}

// Flush keeps an unacknowledged batch unchanged. Its source/sequence lets
// the core acknowledge a retry without counting a delta twice.
func (r *Registry) Flush(ctx context.Context) error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	if !r.client.Connected() {
		return nil
	}
	r.mu.Lock()
	if r.pending == nil {
		points := []Point{}
		for _, p := range r.names {
			if p.dirty || p.point.Kind == "value" {
				points = append(points, p.point)
				p.point.Delta = 0
				p.dirty = false
			}
		}
		if len(points) == 0 && r.dropped == 0 {
			r.mu.Unlock()
			return nil
		}
		r.sequence++
		r.pending = &Batch{Source: r.source, Sequence: r.sequence, Points: points, Dropped: r.dropped}
		r.dropped = 0
	}
	batch := *r.pending
	r.mu.Unlock()
	if err := r.client.Call(ctx, "sdk.metrics", batch, nil); err != nil {
		return err
	}
	r.mu.Lock()
	r.pending = nil
	r.mu.Unlock()
	return nil
}

func (r *Registry) Run(ctx context.Context) {
	timer := time.NewTicker(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			flush, cancel := context.WithTimeout(ctx, 5*time.Second)
			r.Flush(flush)
			cancel()
		}
	}
}
