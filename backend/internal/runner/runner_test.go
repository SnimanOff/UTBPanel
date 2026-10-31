package runner

import (
	"context"
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

var testBot string

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
	_, err := e.db.W.Exec(`INSERT INTO bots (id, name, port, current_version, created_at) VALUES (?, ?, ?, 'v1', 'x')`, id, "bot"+strconv.FormatInt(id, 10), port)
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
