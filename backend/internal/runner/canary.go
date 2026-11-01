package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CanaryEnv marks the second instance for the bot's own code.
const CanaryEnv = "TGPANEL_CANARY"

// ErrCanaryRunning means the bot already has a canary.
var ErrCanaryRunning = errors.New("runner: the canary is already running")

// StartCanary starts the second instance of a bot with another version:
// its own PORT (b.Port is the canary port) and proxy port, the same uid,
// directory and database. A crash is reported, not restarted: the core
// decides whether to cancel the canary.
func (r *Runner) StartCanary(ctx context.Context, b Bot) error {
	b.Instance = "canary"
	r.mu.Lock()
	if _, ok := r.canaries[b.ID]; ok {
		r.mu.Unlock()
		return ErrCanaryRunning
	}
	r.mu.Unlock()
	bin := r.Binary(b.ID, b.Version)
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("runner: binary of version %s: %w", b.Version, err)
	}
	if r.cfg.Hooks.Prepare != nil {
		if err := r.cfg.Hooks.Prepare(ctx, b); err != nil {
			return err
		}
	}
	env, err := r.Env(ctx, b)
	if err != nil {
		return err
	}
	cmd := r.command(b, bin)
	cmd.Env = append(env, CanaryEnv+"=1")
	cmd.Dir = filepath.Join(r.BotDir(b.ID), "files")
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("runner: start canary: %w", err)
	}
	p := &proc{bot: b, cmd: cmd, started: time.Now(), done: make(chan struct{})}
	r.mu.Lock()
	if r.canaries == nil {
		r.canaries = map[int64]*proc{}
	}
	r.canaries[b.ID] = p
	r.mu.Unlock()
	var outWG sync.WaitGroup
	outWG.Add(2)
	go func() { defer outWG.Done(); r.pipe(b.ID, "canary", "stdout", stdout) }()
	go func() { defer outWG.Done(); r.pipe(b.ID, "canary", "stderr", stderr) }()
	go func() {
		outWG.Wait()
		p.exit = cmd.Wait()
		close(p.done)
		r.mu.Lock()
		if r.canaries[b.ID] == p {
			delete(r.canaries, b.ID)
		}
		stopping := p.stopping
		r.mu.Unlock()
		if !stopping {
			r.coreLine(b.ID, "canary %s exited: %v", b.Version, p.exit)
			r.emit(b.ID, "canary_crashed", "error", map[string]any{"version": b.Version, "exit": exitCode(p.exit), "error": fmt.Sprint(p.exit)})
		}
	}()
	r.coreLine(b.ID, "starting canary %s (pid %d, port %d)", b.Version, cmd.Process.Pid, b.Port)
	if err := r.waitReady(ctx, b, p.done); err != nil {
		r.coreLine(b.ID, "canary not ready: %v", err)
		r.mu.Lock()
		p.stopping = true
		r.mu.Unlock()
		if err != ErrExited {
			r.kill(p)
		}
		return err
	}
	r.coreLine(b.ID, "canary %s ready on port %d", b.Version, b.Port)
	r.emit(b.ID, "canary_started", "info", map[string]any{"version": b.Version, "pid": cmd.Process.Pid})
	return nil
}

// StopCanary stops the canary of a bot, if any.
func (r *Runner) StopCanary(id int64) {
	r.mu.Lock()
	p, ok := r.canaries[id]
	if ok {
		p.stopping = true
	}
	r.mu.Unlock()
	if ok {
		r.terminate(p)
	}
}

// Canary returns the running canary of a bot.
func (r *Runner) Canary(id int64) (Bot, Info, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.canaries[id]
	if !ok || p.cmd.Process == nil {
		return Bot{}, Info{}, false
	}
	return p.bot, Info{PID: p.cmd.Process.Pid, Started: p.started}, true
}
