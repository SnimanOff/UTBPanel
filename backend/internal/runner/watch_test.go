package runner

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeProc struct {
	mu sync.Mutex
	u  Usage
}

func (f *fakeProc) Usage(int) (Usage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.u, nil
}

func (f *fakeProc) DirSize(string) (int64, int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.u.FilesTotal, f.u.BiggestFile
}

func (f *fakeProc) set(u Usage) {
	f.mu.Lock()
	f.u = u
	f.mu.Unlock()
}

func countEvents(e *env, typ string) int {
	n := 0
	for _, ev := range e.eventList() {
		if ev == typ {
			n++
		}
	}
	return n
}

func TestLimitsWatcher(t *testing.T) {
	e := newEnv(t)
	fp := &fakeProc{}
	e.r.cfg.Proc = fp
	b := e.addBot(t, 1, Settings{Limits: Limits{OpenFiles: 100}})
	if err := e.r.Start(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	// 85 % of open files: one warning, not repeated within the hour.
	fp.set(Usage{OpenFiles: 85, RSS: 10 << 20})
	e.r.watchOnce()
	e.r.watchOnce()
	time.Sleep(50 * time.Millisecond)
	if n := countEvents(e, "limit_warning"); n != 1 {
		t.Fatalf("warnings = %d", n)
	}
	// 100 %: reached, once.
	fp.set(Usage{OpenFiles: 100, RSS: 10 << 20})
	e.r.watchOnce()
	e.r.watchOnce()
	time.Sleep(50 * time.Millisecond)
	if n := countEvents(e, "limit_reached"); n != 1 {
		t.Fatalf("reached = %d", n)
	}
	if u, ok := e.r.Usage(1); !ok || u.OpenFiles != 100 {
		t.Fatalf("usage = %+v", u)
	}
	// Memory over the limit: the bot is restarted.
	pid1, _ := e.r.Info(1)
	fp.set(Usage{RSS: 300 << 20})
	e.r.watchOnce()
	fp.set(Usage{RSS: 10 << 20})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if info, ok := e.r.Info(1); ok && info.PID != pid1.PID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bot not restarted after going over the memory limit")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(e.output(), "memory 300 MB of 256 MB") {
		t.Fatalf("no core line:\n%s", e.output())
	}
}
