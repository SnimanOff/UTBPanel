package panics

import (
	"strings"
	"sync"
	"testing"
	"time"
)

const indexPanic = `panic: runtime error: index out of range [5] with length 3

goroutine 18 [running]:
github.com/me/echo/internal/handlers.(*Bot).onMessage(0xc000010000, {0x7f1, 0x2})
	/src/internal/handlers/message.go:42 +0x1d4
github.com/go-telegram/bot.(*Bot).ProcessUpdate(0xc0000a4000, {0x7f1, 0x2})
	/go/pkg/mod/github.com/go-telegram/bot@v1.8.0/process.go:30 +0x11
net/http.HandlerFunc.ServeHTTP(0xc000123, {0x7f9, 0xc00})
	/usr/local/go/src/net/http/server.go:2220 +0x29
created by net/http.(*Server).Serve in goroutine 1
	/usr/local/go/src/net/http/server.go:3360 +0x485`

func TestAnalyze(t *testing.T) {
	p := Analyze(strings.Split(indexPanic, "\n"), "github.com/me/echo")
	if p.Kind != "runtime error: index out of range with length N" {
		t.Fatalf("kind %q", p.Kind)
	}
	if p.Frame != "github.com/me/echo/internal/handlers.(*Bot).onMessage (message.go:42)" {
		t.Fatalf("frame %q", p.Frame)
	}
	// Another index and another line: the same group.
	other := strings.Replace(strings.Replace(indexPanic, "[5] with length 3", "[9] with length 1", 1), "message.go:42", "message.go:47", 1)
	if q := Analyze(strings.Split(other, "\n"), "github.com/me/echo"); q.Signature != p.Signature {
		t.Fatalf("%q != %q", q.Signature, p.Signature)
	}
	// Without the module path the first non-standard frame is taken.
	if q := Analyze(strings.Split(indexPanic, "\n"), ""); !strings.HasPrefix(q.Frame, "github.com/me/echo/") {
		t.Fatalf("frame %q", q.Frame)
	}
	main := "panic: boom 42\n\ngoroutine 1 [running]:\nmain.main()\n\t/src/main.go:10 +0x25\nexit status 2"
	if q := Analyze(strings.Split(main, "\n"), ""); q.Kind != "boom N" || q.Frame != "main.main (main.go:10)" {
		t.Fatalf("%+v", q)
	}
}

func TestParserBlocks(t *testing.T) {
	var mu sync.Mutex
	var got []Panic
	p := &Parser{Module: func() string { return "github.com/me/echo" }, Found: func(pn Panic) {
		mu.Lock()
		got = append(got, pn)
		mu.Unlock()
	}}
	now := time.Now()
	p.Line(1, "INFO handling update", now)
	for _, l := range strings.Split(indexPanic, "\n") {
		p.Line(1, l, now)
	}
	// The next ordinary line ends the block.
	p.Line(1, "testbot starting", now)
	// A fatal error ended by the process exit.
	p.Line(1, "fatal error: concurrent map writes", now)
	p.Line(1, "", now)
	p.Line(1, "goroutine 7 [running]:", now)
	p.Line(1, "main.save(...)", now)
	p.Line(1, "\t/src/main.go:20", now)
	p.Flush()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("blocks %d", len(got))
	}
	kinds := got[0].Kind + "|" + got[1].Kind
	if !strings.Contains(kinds, "fatal error: concurrent map writes") || !strings.Contains(kinds, "index out of range") {
		t.Fatalf("%s", kinds)
	}
	for _, g := range got {
		if strings.Contains(g.Stack, "testbot starting") || strings.Contains(g.Stack, "INFO handling") {
			t.Fatalf("foreign lines in the stack:\n%s", g.Stack)
		}
	}
}
