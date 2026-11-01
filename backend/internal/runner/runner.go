// Package runner starts bots as child processes of the core: one process
// per bot, under its own uid, with its settings in environment variables.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// UIDBase: a bot runs as uid and gid 10000 + id.
const UIDBase = 10000

// Timeouts of the life cycle.
var (
	ReadyTimeout = 15 * time.Second
	StopTimeout  = 10 * time.Second
)

// States stored in core.db.
const (
	Running = "running"
	Stopped = "stopped"
	Paused  = "paused"
	Crashed = "crashed"
	Errored = "error"
)

// Settings is the part of bots.settings the runner reads.
type Settings struct {
	Env map[string]string `json:"env,omitempty"`
	// Rename maps standard variable names to the ones the bot's code reads,
	// e.g. BOT_TOKEN → TELEGRAM_TOKEN.
	Rename map[string]string `json:"rename,omitempty"`
	Limits Limits            `json:"limits,omitzero"`
}

// Bot is what the runner needs to know to start a bot.
type Bot struct {
	ID       int64
	Name     string
	Port     int
	Version  string
	Settings Settings
	// Instance is "" for the main process and "canary" for the second one.
	Instance string
}

// Line is one line of bot output.
type Line struct {
	BotID    int64
	Stream   string // stdout | stderr | core
	Text     string
	Time     time.Time
	Instance string // "" or "canary"
}

// Hooks lets the core react to the life cycle.
type Hooks struct {
	// Prepare must succeed before launching either process instance.
	Prepare func(context.Context, Bot) error
	// Readiness reports whether this instance requires an explicit SDK signal.
	Readiness func(Bot) (usesSDK, ready bool)
	// Output receives every line the bot prints.
	Output func(Line)
	// Event reports life cycle events (bot_started, bot_crashed…).
	Event func(botID int64, typ, severity string, data map[string]any)
	// Env adds variables right before start (proxy, SDK…).
	Env func(b Bot, env map[string]string)
	// Started runs after the bot accepts connections (sets the webhook).
	Started func(ctx context.Context, b Bot)
	// Stopping runs before a stop by the owner (removes the webhook).
	Stopping func(ctx context.Context, b Bot)
	// Usage receives every measurement of the limits watcher (counters).
	Usage func(botID int64, u Usage)
}

// Config of the runner.
type Config struct {
	DataDir    string // /data/core
	WebhookURL func(botID int64) string
	Secrets    *secrets.Store
	DB         *store.DB
	Log        *slog.Logger
	Hooks      Hooks
	// DropPrivileges runs bots under their uid; set when the core is root.
	DropPrivileges bool
	// ExecSelf is the core binary: with DropPrivileges a bot is started as
	// `core exec-bot`, which sets limits and the uid and then becomes the bot.
	ExecSelf        string
	DefaultLimits   Limits
	SDKReadyTimeout time.Duration
	// Proc reads /proc; nil means the real one.
	Proc ProcReader
}

// Runner manages bot processes.
type Runner struct {
	cfg Config
	log *slog.Logger

	mu       sync.Mutex
	procs    map[int64]*proc
	canaries map[int64]*proc
	sups     map[int64]*supervisor
	watch    watchState
}

type proc struct {
	bot     Bot
	cmd     *exec.Cmd
	started time.Time
	done    chan struct{}
	exit    error
	// stopping is set when the owner stops the bot: its exit is not a crash.
	stopping bool
}

// New returns a runner.
func New(cfg Config) *Runner {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Runner{cfg: cfg, log: cfg.Log, procs: map[int64]*proc{}, watch: watchState{
		usage: map[int64]Usage{}, warned: map[string]time.Time{}, reached: map[string]bool{}, lastDisk: map[int64]time.Time{},
	}}
}

// BotDir is /data/core/bots/<id>.
func (r *Runner) BotDir(id int64) string {
	return filepath.Join(r.cfg.DataDir, "bots", strconv.FormatInt(id, 10))
}

// Binary is the path of a release binary.
func (r *Runner) Binary(id int64, version string) string {
	return filepath.Join(r.BotDir(id), "releases", version, "bot")
}

// UID returns the uid of a bot.
func UID(id int64) int { return UIDBase + int(id) }

// ErrNoVersion means no binary was uploaded yet.
var ErrNoVersion = errors.New("runner: no version uploaded yet")

// ErrRunning is returned when starting a running bot.
var ErrRunning = errors.New("runner: bot is already running")

// IsRunning reports whether the bot process is alive.
func (r *Runner) IsRunning(id int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.procs[id]
	return ok
}

// Info about a running process.
type Info struct {
	PID     int
	Started time.Time
}

// Info returns the running process of a bot.
func (r *Runner) Info(id int64) (Info, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.procs[id]
	if !ok || p.cmd.Process == nil {
		return Info{}, false
	}
	return Info{PID: p.cmd.Process.Pid, Started: p.started}, true
}

func (r *Runner) emit(id int64, typ, sev string, data map[string]any) {
	if r.cfg.Hooks.Event != nil {
		r.cfg.Hooks.Event(id, typ, sev, data)
	}
}

func (r *Runner) coreLine(id int64, format string, args ...any) {
	if r.cfg.Hooks.Output != nil {
		r.cfg.Hooks.Output(Line{BotID: id, Stream: "core", Text: fmt.Sprintf(format, args...), Time: time.Now().UTC()})
	}
}

// Env builds the environment of a bot (the standard contract).
func (r *Runner) Env(ctx context.Context, b Bot) ([]string, error) {
	secs, err := r.cfg.Secrets.DecryptAll(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	dir := r.BotDir(b.ID)
	uid := -1
	if r.cfg.DropPrivileges {
		uid = UID(b.ID)
	}
	files, err := secrets.WriteFiles(filepath.Join(dir, "secrets"), uid, secs)
	if err != nil {
		return nil, err
	}
	env := map[string]string{
		"PORT":         strconv.Itoa(b.Port),
		"DATABASE_URL": "file:" + filepath.Join(dir, "bot.db"),
		"DATA_DIR":     filepath.Join(dir, "files"),
		"HOME":         filepath.Join(dir, "files"),
		"TZ":           "UTC",
	}
	if r.cfg.WebhookURL != nil {
		env["WEBHOOK_URL"] = r.cfg.WebhookURL(b.ID)
	}
	for k, v := range b.Settings.Env {
		env[k] = v
	}
	for _, s := range secs {
		if s.Kind == secrets.KindFile {
			env[s.Name] = files[s.Name]
		} else {
			env[s.Name] = string(s.Value)
		}
	}
	env["GOMEMLIMIT"] = b.Settings.Limits.Or(r.cfg.DefaultLimits).GoMemLimit()
	if r.cfg.Hooks.Env != nil {
		r.cfg.Hooks.Env(b, env)
	}
	for from, to := range b.Settings.Rename {
		if v, ok := env[from]; ok && to != "" {
			delete(env, from)
			env[to] = v
		}
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out, nil
}

// prepareDirs creates the bot directory owned by the bot's uid.
func (r *Runner) prepareDirs(id int64) error {
	dir := r.BotDir(id)
	if r.cfg.DropPrivileges {
		// Bots may pass through /data/core and bots/ but not list them: they
		// see only their own directory, never core.db or other bots.
		for _, d := range []string{r.cfg.DataDir, filepath.Join(r.cfg.DataDir, "bots")} {
			if err := os.MkdirAll(d, 0o711); err != nil {
				return err
			}
			os.Chmod(d, 0o711)
		}
	}
	for _, d := range []string{dir, filepath.Join(dir, "files"), filepath.Join(dir, "releases")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
		if r.cfg.DropPrivileges {
			os.Chmod(d, 0o750)
			if err := os.Chown(d, UID(id), UID(id)); err != nil {
				return err
			}
		}
	}
	// The SQLite files of the bot must be writable by it.
	if r.cfg.DropPrivileges {
		for _, f := range []string{"bot.db", "bot.db-wal", "bot.db-shm"} {
			os.Chown(filepath.Join(dir, f), UID(id), UID(id))
		}
	}
	return nil
}

// Start launches the bot and waits until it accepts connections on PORT.
func (r *Runner) Start(ctx context.Context, b Bot) error {
	if b.Version == "" {
		return ErrNoVersion
	}
	r.mu.Lock()
	if _, ok := r.procs[b.ID]; ok {
		r.mu.Unlock()
		return ErrRunning
	}
	r.mu.Unlock()
	bin := r.Binary(b.ID, b.Version)
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("runner: binary of version %s: %w", b.Version, err)
	}
	if err := r.prepareDirs(b.ID); err != nil {
		return err
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
	cmd.Env = env
	cmd.Dir = filepath.Join(r.BotDir(b.ID), "files")
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("runner: start: %w", err)
	}
	p := &proc{bot: b, cmd: cmd, started: time.Now(), done: make(chan struct{})}
	r.mu.Lock()
	r.procs[b.ID] = p
	r.mu.Unlock()
	var outWG sync.WaitGroup
	outWG.Add(2)
	go func() { defer outWG.Done(); r.pipe(b.ID, "", "stdout", stdout) }()
	go func() { defer outWG.Done(); r.pipe(b.ID, "", "stderr", stderr) }()
	go func() {
		outWG.Wait() // read all output before Wait closes the pipes
		p.exit = cmd.Wait()
		close(p.done)
		r.exited(p)
	}()
	r.coreLine(b.ID, "starting version %s (pid %d)", b.Version, cmd.Process.Pid)

	if err := r.waitReady(ctx, b, p.done); err != nil {
		r.coreLine(b.ID, "not ready: %v", err)
		if err != ErrExited {
			r.mu.Lock()
			p.stopping = true
			r.mu.Unlock()
			r.kill(p)
			r.setState(ctx, b.ID, Crashed)
		}
		return err
	}
	if r.inLoop(b.ID) {
		// Still "error" until it survives the crash window.
		time.AfterFunc(loopWindow, func() {
			r.mu.Lock()
			alive := r.procs[b.ID] == p
			r.mu.Unlock()
			if alive {
				r.markRunning(context.Background(), b.ID)
			}
		})
	} else {
		r.markRunning(ctx, b.ID)
	}
	r.coreLine(b.ID, "ready on port %d", b.Port)
	r.emit(b.ID, "bot_started", "info", map[string]any{"version": b.Version, "pid": cmd.Process.Pid})
	if r.cfg.Hooks.Started != nil {
		r.cfg.Hooks.Started(ctx, b)
	}
	return nil
}

// ErrExited means the bot exited before it became ready.
var ErrExited = errors.New("runner: bot exited before it was ready")

func waitPort(ctx context.Context, port int, done <-chan struct{}, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("runner: port %d not open after %v", port, timeout)
		}
		select {
		case <-done:
			return ErrExited
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Stop stops the bot: SIGTERM to its process group, SIGKILL after 10 s.
func (r *Runner) Stop(ctx context.Context, id int64) error {
	r.cancelRestart(id)
	r.mu.Lock()
	p, ok := r.procs[id]
	if ok {
		p.stopping = true
	}
	r.mu.Unlock()
	if !ok {
		r.setState(ctx, id, Stopped)
		return nil
	}
	if r.cfg.Hooks.Stopping != nil {
		r.cfg.Hooks.Stopping(ctx, p.bot)
	}
	r.terminate(p)
	r.setState(ctx, id, Stopped)
	r.emit(id, "bot_stopped", "info", nil)
	return nil
}

// Shutdown stops all bots without changing their saved state, so they
// come back after the core restarts. Webhooks stay in place.
func (r *Runner) Shutdown() {
	r.mu.Lock()
	ps := make([]*proc, 0, len(r.procs))
	for _, p := range r.procs {
		p.stopping = true
		ps = append(ps, p)
	}
	for _, p := range r.canaries {
		p.stopping = true
		ps = append(ps, p)
	}
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range ps {
		wg.Go(func() { r.terminate(p) })
	}
	wg.Wait()
}

func (r *Runner) terminate(p *proc) {
	signalTerm(p.cmd)
	select {
	case <-p.done:
		r.coreLine(p.bot.ID, "stopped")
	case <-time.After(StopTimeout):
		r.coreLine(p.bot.ID, "did not stop in %v, killing", StopTimeout)
		r.kill(p)
	}
}

func (r *Runner) kill(p *proc) {
	signalKill(p.cmd)
	<-p.done
}

func (r *Runner) exited(p *proc) {
	r.mu.Lock()
	if r.procs[p.bot.ID] == p {
		delete(r.procs, p.bot.ID)
	}
	stopping := p.stopping
	r.mu.Unlock()
	if stopping {
		return
	}
	code := exitCode(p.exit)
	r.coreLine(p.bot.ID, "exited: %v", p.exit)
	r.emit(p.bot.ID, "bot_crashed", "error", map[string]any{"exit": code, "error": fmt.Sprint(p.exit), "uptime_s": int(time.Since(p.started).Seconds())})
	r.crashed(p, code)
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

func (r *Runner) setState(ctx context.Context, id int64, state string) {
	if r.cfg.DB == nil {
		return
	}
	if _, err := r.cfg.DB.W.ExecContext(ctx, `UPDATE bots SET state = ? WHERE id = ?`, state, id); err != nil {
		r.log.Error("saving bot state failed", "bot", id, "state", state, "err", err)
	}
}

// markRunning saves "running" unless the bot is paused: a paused bot's
// process runs, but updates do not reach it.
func (r *Runner) markRunning(ctx context.Context, id int64) {
	if r.cfg.DB == nil {
		return
	}
	if _, err := r.cfg.DB.W.ExecContext(ctx, `UPDATE bots SET state = 'running' WHERE id = ? AND state != 'paused'`, id); err != nil {
		r.log.Error("saving bot state failed", "bot", id, "err", err)
	}
}

// LoadBot reads what the runner needs from core.db.
func LoadBot(ctx context.Context, db *store.DB, id int64) (Bot, error) {
	var b Bot
	var settings string
	err := db.R.QueryRowContext(ctx, `SELECT id, name, port, current_version, settings FROM bots WHERE id = ?`, id).
		Scan(&b.ID, &b.Name, &b.Port, &b.Version, &settings)
	if err != nil {
		return b, err
	}
	json.Unmarshal([]byte(settings), &b.Settings)
	return b, nil
}
