package runner

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

var testBot, execBot string

func TestMain(m *testing.M) {
	if runtime.GOOS == "windows" {
		// Bots are Linux binaries; run these tests with `make test-linux`.
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "testbot")
	if err != nil {
		panic(err)
	}
	testBot = filepath.Join(dir, "bot")
	build := exec.Command("go", "build", "-o", testBot, "./testdata/testbot")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		panic(string(out))
	}
	execBot = filepath.Join(dir, "execbot")
	build = exec.Command("go", "build", "-o", execBot, "./testdata/execbot")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type env struct {
	r      *Runner
	db     *store.DB
	sec    *secrets.Store
	mu     sync.Mutex
	lines  []Line
	events []string
}

func (e *env) output() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var b strings.Builder
	for _, l := range e.lines {
		b.WriteString(l.Stream + ": " + l.Text + "\n")
	}
	return b.String()
}

func (e *env) eventList() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.events...)
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	os.Chmod(dir, 0o711) // bots under their own uid pass through it
	os.Chmod(filepath.Dir(dir), 0o711)
	db, err := store.Open(ctx, filepath.Join(dir, "core.db"), migrations.Core())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key, _ := secrets.LoadMasterKey(t.TempDir())
	box, _ := secrets.NewBox(key)
	e := &env{db: db, sec: &secrets.Store{DB: db, Box: box, Redact: secrets.NewRedactor()}}
	e.r = New(Config{
		DataDir: filepath.Join(dir, "data"), DB: db, Secrets: e.sec,
		WebhookURL:     func(id int64) string { return "https://203.0.113.10:8443/hook/" + strconv.FormatInt(id, 10) },
		DropPrivileges: os.Getenv("RUNNER_AS_ROOT") == "1",
		ExecSelf:       execBot,
		DefaultLimits:  Limits{Memory: 256 << 20, File: 1 << 30, FilesTotal: 2 << 30, OpenFiles: 4096, Threads: 1024},
		Hooks: Hooks{
			Output: func(l Line) { e.mu.Lock(); e.lines = append(e.lines, l); e.mu.Unlock() },
			Event: func(id int64, typ, sev string, _ map[string]any) {
				e.mu.Lock()
				e.events = append(e.events, typ)
				e.mu.Unlock()
			},
		},
	})
	t.Cleanup(e.r.Shutdown)
	return e
}

// addBot registers a bot with the test binary as its version.
func (e *env) addBot(t *testing.T, id int64, settings Settings) Bot {
	t.Helper()
	port := freePort(t)
	js, _ := json.Marshal(settings)
	_, err := e.db.W.Exec(`INSERT INTO bots (id, name, port, current_version, settings, created_at) VALUES (?, ?, ?, 'v1', ?, 'x')`, id, "bot"+strconv.FormatInt(id, 10), port, string(js))
	if err != nil {
		t.Fatal(err)
	}
	bin := e.r.Binary(id, "v1")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	data, _ := os.ReadFile(testBot)
	if err := os.WriteFile(bin, data, 0o755); err != nil {
		t.Fatal(err)
	}
	e.sec.Set(context.Background(), id, secrets.TokenName, secrets.KindValue, []byte("111:token"))
	e.sec.Set(context.Background(), id, secrets.WebhookSecretName, secrets.KindValue, []byte("hook-secret"))
	return Bot{ID: id, Name: "bot", Port: port, Version: "v1", Settings: settings}
}

func get(t *testing.T, port int, path string) string {
	t.Helper()
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func state(t *testing.T, db *store.DB, id int64) string {
	var s string
	db.R.QueryRow(`SELECT state FROM bots WHERE id = ?`, id).Scan(&s)
	return s
}

func TestStartStopAndEnv(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	b := e.addBot(t, 1, Settings{Env: map[string]string{"LOG_LEVEL": "debug"}, Rename: map[string]string{"BOT_TOKEN": "TELEGRAM_TOKEN"}})
	if err := e.r.Start(ctx, b); err != nil {
		t.Fatal(err)
	}
	if state(t, e.db, 1) != Running || !e.r.IsRunning(1) {
		t.Fatal("not running")
	}
	env := get(t, b.Port, "/env")
	for _, want := range []string{
		"TELEGRAM_TOKEN=111:token", "WEBHOOK_SECRET=hook-secret", "PORT=" + strconv.Itoa(b.Port),
		"WEBHOOK_URL=https://203.0.113.10:8443/hook/1", "DATABASE_URL=file:", "DATA_DIR=", "LOG_LEVEL=debug",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	if strings.Contains(env, "\nBOT_TOKEN=") {
		t.Error("renamed variable kept its standard name")
	}
	if err := e.r.Start(ctx, b); err != ErrRunning {
		t.Fatalf("second start: %v", err)
	}
	if err := e.r.Stop(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if state(t, e.db, 1) != Stopped || e.r.IsRunning(1) {
		t.Fatal("still running")
	}
	out := e.output()
	if !strings.Contains(out, "stdout: testbot starting") || !strings.Contains(out, "stderr: to stderr") {
		t.Fatalf("output not captured:\n%s", out)
	}
	evs := strings.Join(e.eventList(), ",")
	if evs != "bot_started,bot_stopped" {
		t.Fatalf("events = %s", evs)
	}
}

func TestCrashBeforeReady(t *testing.T) {
	e := newEnv(t)
	b := e.addBot(t, 1, Settings{Env: map[string]string{"MODE": "crash"}})
	err := e.r.Start(context.Background(), b)
	if err != ErrExited {
		t.Fatalf("got %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for state(t, e.db, 1) != Crashed {
		if time.Now().After(deadline) {
			t.Fatalf("state = %s", state(t, e.db, 1))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestNoVersion(t *testing.T) {
	e := newEnv(t)
	if err := e.r.Start(context.Background(), Bot{ID: 9}); err != ErrNoVersion {
		t.Fatalf("got %v", err)
	}
}

func waitState(t *testing.T, db *store.DB, id int64, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for state(t, db, id) != want {
		if time.Now().After(deadline) {
			t.Fatalf("state = %s, want %s", state(t, db, id), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func shortBackoff(t *testing.T) {
	oldMin, oldMax, oldWin := backoffMin, backoffMax, loopWindow
	backoffMin, backoffMax, loopWindow = 50*time.Millisecond, 200*time.Millisecond, time.Minute
	t.Cleanup(func() { backoffMin, backoffMax, loopWindow = oldMin, oldMax, oldWin })
}

func TestRestartAfterCrashAndLoop(t *testing.T) {
	shortBackoff(t)
	e := newEnv(t)
	b := e.addBot(t, 1, Settings{Env: map[string]string{"MODE": "crashlater"}})
	if err := e.r.Start(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	// It keeps crashing: restarts happen and after five crashes → error.
	waitState(t, e.db, 1, Errored)
	st := e.r.Status(1)
	if st.Restarts < 4 || st.LastExit != 3 {
		t.Fatalf("status = %+v", st)
	}
	evs := strings.Join(e.eventList(), ",")
	if !strings.Contains(evs, "bot_crashed") || !strings.Contains(evs, "bot_restarted") || !strings.Contains(evs, "bot_crash_loop") {
		t.Fatalf("events = %s", evs)
	}
	// The owner stops it: no more restarts.
	e.r.Stop(context.Background(), 1)
	time.Sleep(500 * time.Millisecond)
	if e.r.IsRunning(1) || state(t, e.db, 1) != Stopped {
		t.Fatal("restarted after an owner stop")
	}
}

func TestStopKillsStubborn(t *testing.T) {
	old := StopTimeout
	StopTimeout = 300 * time.Millisecond
	t.Cleanup(func() { StopTimeout = old })
	e := newEnv(t)
	b := e.addBot(t, 1, Settings{Env: map[string]string{"MODE": "stubborn"}})
	if err := e.r.Start(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	e.r.Stop(context.Background(), 1)
	if e.r.IsRunning(1) || time.Since(start) > 3*time.Second {
		t.Fatal("stubborn bot not killed")
	}
	if !strings.Contains(e.output(), "killing") {
		t.Fatal("no kill line in the log")
	}
}

func TestRestoreStates(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	for id := int64(1); id <= 3; id++ {
		e.addBot(t, id, Settings{})
	}
	e.db.W.Exec(`UPDATE bots SET state = 'running' WHERE id = 1`)
	e.db.W.Exec(`UPDATE bots SET state = 'paused' WHERE id = 2`)
	e.db.W.Exec(`UPDATE bots SET state = 'stopped' WHERE id = 3`)
	e.r.Restore(ctx)
	if !e.r.IsRunning(1) || !e.r.IsRunning(2) || e.r.IsRunning(3) {
		t.Fatal("wrong bots restored")
	}
	if state(t, e.db, 2) != Paused || state(t, e.db, 1) != Running {
		t.Fatalf("states %s %s", state(t, e.db, 1), state(t, e.db, 2))
	}
	// Core shutdown keeps the saved state.
	e.r.Shutdown()
	if state(t, e.db, 1) != Running {
		t.Fatal("shutdown changed the saved state")
	}
}

func TestLimitsApplied(t *testing.T) {
	if os.Getenv("RUNNER_AS_ROOT") != "1" {
		t.Skip("needs root in a Linux container")
	}
	e := newEnv(t)
	b := e.addBot(t, 7, Settings{Limits: Limits{OpenFiles: 777}})
	if err := e.r.Start(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	limits := get(t, b.Port, "/limits")
	for _, want := range []string{"Max open files            777", "Max core file size        0", "Max processes             1024", "Max file size             1073741824"} {
		if !strings.Contains(strings.Join(strings.Fields(limits), " "), strings.Join(strings.Fields(want), " ")) {
			t.Errorf("limit missing: %s\n%s", want, limits)
		}
	}
	if who := get(t, b.Port, "/whoami"); who != "10007 10007" {
		t.Errorf("uid = %s", who)
	}
	pid, _ := e.r.Info(7)
	adj, _ := os.ReadFile("/proc/" + strconv.Itoa(pid.PID) + "/oom_score_adj")
	if strings.TrimSpace(string(adj)) != "1000" {
		t.Errorf("oom_score_adj = %s", adj)
	}
	if env := get(t, b.Port, "/env"); !strings.Contains(env, "GOMEMLIMIT=241591910") {
		t.Errorf("GOMEMLIMIT missing")
	}
}

// A bot sees only its own directory: not core.db, not another bot.
func TestIsolation(t *testing.T) {
	if os.Getenv("RUNNER_AS_ROOT") != "1" {
		t.Skip("needs root in a Linux container")
	}
	ctx := context.Background()
	e := newEnv(t)
	b1 := e.addBot(t, 1, Settings{})
	e.addBot(t, 2, Settings{})
	os.MkdirAll(filepath.Join(e.r.BotDir(2), "files"), 0o750)
	os.WriteFile(filepath.Join(e.r.BotDir(2), "files", "data.txt"), []byte("secret of bot 2"), 0o640)
	e.r.prepareDirs(2)
	if err := e.r.Start(ctx, b1); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		e.db.Path,
		filepath.Join(e.r.BotDir(2), "files", "data.txt"),
		e.r.BotDir(2),
	} {
		if got := get(t, b1.Port, "/read?path="+path); !strings.Contains(got, "permission denied") {
			t.Errorf("%s readable by another uid: %s", path, got)
		}
	}
	// Its own data dir is fine.
	own := filepath.Join(e.r.BotDir(1), "files", "mine.txt")
	os.WriteFile(own, []byte("x"), 0o600)
	os.Chown(own, UID(1), UID(1))
	if got := get(t, b1.Port, "/read?path="+own); got != "<nil>" {
		t.Errorf("own file: %s", got)
	}
}

func TestCanary(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	b := e.addBot(t, 1, Settings{})
	if err := e.r.Start(ctx, b); err != nil {
		t.Fatal(err)
	}
	cb := b
	cb.Port = freePort(t)
	if err := e.r.StartCanary(ctx, cb); err != nil {
		t.Fatal(err)
	}
	if err := e.r.StartCanary(ctx, cb); err != ErrCanaryRunning {
		t.Fatalf("second canary: %v", err)
	}
	env := get(t, cb.Port, "/env")
	if !strings.Contains(env, "TGPANEL_CANARY=1") || !strings.Contains(env, "PORT="+strconv.Itoa(cb.Port)) {
		t.Fatalf("canary env:\n%s", env)
	}
	if strings.Contains(get(t, b.Port, "/env"), "TGPANEL_CANARY") {
		t.Fatal("main instance marked as canary")
	}
	if _, _, ok := e.r.Canary(1); !ok {
		t.Fatal("canary not listed")
	}
	e.r.StopCanary(1)
	if _, _, ok := e.r.Canary(1); ok || !e.r.IsRunning(1) {
		t.Fatal("stopping the canary must leave the main instance")
	}
	for _, ev := range e.eventList() {
		if ev == "canary_crashed" {
			t.Fatal("a stop is not a crash")
		}
	}
	e.mu.Lock()
	tagged := false
	for _, l := range e.lines {
		tagged = tagged || l.Instance == "canary"
	}
	e.mu.Unlock()
	if !tagged {
		t.Fatal("canary output not tagged")
	}
}
