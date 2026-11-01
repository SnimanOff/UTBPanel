// Package tasks runs UTC schedules locally without a core connection and
// registers them with the core when connected. Canary instances never own
// schedules. Functions should stop when their context is canceled.
package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

const MaxTasks = 100

var taskName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]{0,63}$`)

func Validate(name, schedule string) error {
	if !taskName.MatchString(name) {
		return fmt.Errorf("sdk: invalid task name %q", name)
	}
	if strings.HasPrefix(schedule, "TZ=") || strings.HasPrefix(schedule, "CRON_TZ=") {
		return fmt.Errorf("sdk: schedules use UTC")
	}
	_, err := cron.ParseStandard(schedule)
	return err
}

type Definition struct {
	Name string `cbor:"1,keyasint" json:"name"`
	Cron string `cbor:"2,keyasint" json:"cron"`
}
type RunRequest struct {
	Name string `cbor:"1,keyasint"`
	ID   int64  `cbor:"2,keyasint,omitempty"`
}
type Caller interface {
	Call(context.Context, string, any, any) error
}
type Client interface {
	Caller
	Connected() bool
	Use(string) error
}
type task struct {
	definition Definition
	fn         func(context.Context) error
	running    bool
	local      bool
	cancel     context.CancelFunc
}
type Registry struct {
	mu         sync.Mutex
	registerMu sync.Mutex
	client     Client
	log        *slog.Logger
	cron       *cron.Cron
	entries    map[string]*task
	canary     bool
	managed    bool
	started    bool
	closed     bool
	ctx        context.Context
	cancel     context.CancelFunc
}

func New(client Client, log *slog.Logger, canary bool) *Registry {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Registry{client: client, log: log, canary: canary, cron: cron.New(cron.WithLocation(time.UTC)), entries: map[string]*task{}, ctx: ctx, cancel: cancel}
}

func (r *Registry) Add(name, schedule string, fn func(context.Context) error) error {
	if err := Validate(name, schedule); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("sdk: nil task function")
	}
	if err := r.client.Use("tasks"); err != nil {
		return err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return fmt.Errorf("sdk: tasks are closed")
	}
	if r.entries[name] != nil || len(r.entries) >= MaxTasks {
		r.mu.Unlock()
		return fmt.Errorf("sdk: duplicate task or task limit reached")
	}
	if _, err := r.cron.AddFunc(schedule, func() {
		if err := r.execute(r.ctx, name, true); err != nil {
			r.log.Warn("sdk: scheduled task failed", "task", name, "err", err)
		}
	}); err != nil {
		r.mu.Unlock()
		return err
	}
	r.entries[name] = &task{definition: Definition{Name: name, Cron: schedule}, fn: fn}
	if !r.started && !r.canary {
		r.started = true
		r.cron.Start()
	}
	r.mu.Unlock()
	if r.client.Connected() {
		ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
		defer cancel()
		return r.register(ctx, r.client)
	}
	return nil
}

func (r *Registry) definitions() []Definition {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Definition, 0, len(r.entries))
	for _, t := range r.entries {
		out = append(out, t.definition)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) register(ctx context.Context, c Caller) error {
	r.registerMu.Lock()
	defer r.registerMu.Unlock()
	defs := r.definitions()
	if len(defs) == 0 {
		return nil
	}
	return c.Call(ctx, "sdk.tasks.register", defs, nil)
}

func (r *Registry) Connected(ctx context.Context, c Caller) error {
	r.mu.Lock()
	r.managed = true
	for _, t := range r.entries {
		if t.local && t.cancel != nil {
			t.cancel()
		}
	}
	r.mu.Unlock()
	if err := r.register(ctx, c); err != nil {
		r.Disconnected()
		return err
	}
	return nil
}
func (r *Registry) Disconnected() { r.mu.Lock(); r.managed = false; r.mu.Unlock() }

func (r *Registry) Run(ctx context.Context, name string) error { return r.execute(ctx, name, false) }
func (r *Registry) execute(ctx context.Context, name string, local bool) (err error) {
	r.mu.Lock()
	t := r.entries[name]
	if r.closed || r.canary {
		r.mu.Unlock()
		if local {
			return nil
		}
		return fmt.Errorf("sdk: task execution is disabled")
	}
	if local && r.managed {
		r.mu.Unlock()
		return nil
	}
	if t == nil {
		r.mu.Unlock()
		return fmt.Errorf("sdk: task %q is not registered", name)
	}
	if t.running {
		r.mu.Unlock()
		return fmt.Errorf("sdk: task %q is already running", name)
	}
	ctx, cancel := context.WithCancel(ctx)
	t.running = true
	t.local = local
	t.cancel = cancel
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		t.running = false
		t.cancel = nil
		r.mu.Unlock()
		if p := recover(); p != nil {
			err = fmt.Errorf("task panic: %v", p)
		}
	}()
	return t.fn(ctx)
}

func (r *Registry) Close() {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	for _, t := range r.entries {
		if t.cancel != nil {
			t.cancel()
		}
	}
	r.mu.Unlock()
	r.cron.Stop()
}
