package safe

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGoRecovers(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), nil))
	done := make(chan struct{})
	Go(log, "worker", func() {
		defer close(done)
		panic("boom")
	})
	<-done
	var out string
	for range 100 { // the panic is logged after fn's own defers ran
		mu.Lock()
		out = buf.String()
		mu.Unlock()
		if out != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(out, "boom") || !strings.Contains(out, "worker") || !strings.Contains(out, "safe_test.go") {
		t.Fatalf("log = %s", out)
	}
	if err := Call(log, "call", func() error { panic("x") }); err == nil {
		t.Fatal("want error")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
