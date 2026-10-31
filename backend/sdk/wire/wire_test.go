package wire

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sockPath returns a short socket path (unix socket paths are limited to
// about 100 bytes, and t.TempDir can be long).
func sockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wire")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

var testHello = Hello{Proto: "test", Version: 2, MinVersion: 1, Caps: []string{"canary", "metrics"}}

type pair struct {
	srv    *Server
	ln     net.Listener
	client *Conn
	path   string
}

func newPair(t *testing.T, mux *Mux) *pair {
	t.Helper()
	path := sockPath(t)
	ln, err := Listen(path, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Mux: mux, Hello: testHello}
	go srv.Serve(ln)
	c, err := Dial(context.Background(), path, nil, testHello, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); ln.Close() })
	return &pair{srv: srv, ln: ln, client: c, path: path}
}

type echoIn struct {
	S string
	N int
	B []byte
}

func echoMux() *Mux {
	m := NewMux()
	m.Handle("echo", Func(func(ctx context.Context, c *Conn, in echoIn) (echoIn, error) {
		return in, nil
	}))
	m.Handle("fail", func(context.Context, *Conn, Raw) (any, error) {
		return nil, Errorf(CodeNotFound, "bot %d not found", 7)
	})
	m.Handle("panic", func(context.Context, *Conn, Raw) (any, error) { panic("boom") })
	m.Handle("sleep", func(ctx context.Context, c *Conn, _ Raw) (any, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
			return "late", nil
		}
	})
	m.HandleStream("count", func(ctx context.Context, c *Conn, body Raw, s *Sender) error {
		var n int
		Unmarshal(body, &n)
		for i := range n {
			if err := s.Send(i); err != nil {
				return err
			}
		}
		return nil
	})
	m.HandleStream("forever", func(ctx context.Context, c *Conn, body Raw, s *Sender) error {
		for i := 0; ; i++ {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.Send(i)
			time.Sleep(time.Millisecond)
		}
	})
	return m
}

func TestCall(t *testing.T) {
	p := newPair(t, echoMux())
	ctx := context.Background()
	var out echoIn
	if err := p.client.Call(ctx, "echo", echoIn{S: "привет", N: 42}, &out); err != nil {
		t.Fatal(err)
	}
	if out.S != "привет" || out.N != 42 {
		t.Fatalf("out = %+v", out)
	}
	err := p.client.Call(ctx, "fail", nil, nil)
	var we *Error
	if !errors.As(err, &we) || we.Code != CodeNotFound || we.Message != "bot 7 not found" {
		t.Fatalf("err = %v", err)
	}
	if err := p.client.Call(ctx, "nope", nil, nil); !errors.As(err, &we) || we.Code != CodeNoMethod {
		t.Fatalf("err = %v", err)
	}
	if err := p.client.Call(ctx, "panic", nil, nil); !errors.As(err, &we) || we.Code != CodeInternal {
		t.Fatalf("err = %v", err)
	}
	// The connection survives a panicking handler.
	if err := p.client.Call(ctx, "echo", echoIn{N: 1}, &out); err != nil {
		t.Fatal(err)
	}
}

func TestParallelCalls(t *testing.T) {
	p := newPair(t, echoMux())
	var wg sync.WaitGroup
	var bad atomic.Int32
	for i := range 200 {
		wg.Go(func() {
			var out echoIn
			if err := p.client.Call(context.Background(), "echo", echoIn{N: i}, &out); err != nil || out.N != i {
				bad.Add(1)
			}
		})
	}
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("%d calls failed", bad.Load())
	}
}

func TestLargeMessage(t *testing.T) {
	p := newPair(t, echoMux())
	big := bytes.Repeat([]byte("x"), 8<<20)
	var out echoIn
	if err := p.client.Call(context.Background(), "echo", echoIn{B: big}, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.B, big) {
		t.Fatal("payload mismatch")
	}
	tooBig := bytes.Repeat([]byte("x"), MaxFrame+1)
	if err := p.client.Call(context.Background(), "echo", echoIn{B: tooBig}, &out); err == nil {
		t.Fatal("want error for oversized frame")
	}
}

func TestCancel(t *testing.T) {
	p := newPair(t, echoMux())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := p.client.Call(ctx, "sleep", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancel took too long")
	}
	// The server side got the deadline and freed the handler.
	deadline := time.Now().Add(time.Second)
	for {
		conns := p.srv.Conns()
		if len(conns) == 1 {
			conns[0].mu.Lock()
			n := len(conns[0].inflight)
			conns[0].mu.Unlock()
			if n == 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("server handler still running")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStream(t *testing.T) {
	p := newPair(t, echoMux())
	s, err := p.client.OpenStream(context.Background(), "count", 1000, StreamOptions{Buffer: 2000})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 1000 {
		var v int
		if err := s.Recv(&v); err != nil || v != i {
			t.Fatalf("recv %d: %v, %v", i, v, err)
		}
	}
	var v int
	if err := s.Recv(&v); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestLossyStream(t *testing.T) {
	p := newPair(t, echoMux())
	ctx, cancel := context.WithCancel(context.Background())
	s, err := p.client.OpenStream(ctx, "forever", nil, StreamOptions{Buffer: 4, Lossy: true})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // do not read: the buffer overflows
	var first, last int
	s.Recv(&first)
	for range 3 {
		s.Recv(&last)
	}
	if s.Dropped() == 0 || first == 0 {
		t.Fatalf("expected old frames to be dropped (first=%d dropped=%d)", first, s.Dropped())
	}
	cancel()
	for {
		var v int
		if err := s.Recv(&v); err != nil {
			break
		}
	}
}

func TestSlowReaderFailsStrictStream(t *testing.T) {
	p := newPair(t, echoMux())
	s, err := p.client.OpenStream(context.Background(), "count", 100, StreamOptions{Buffer: 4})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	var err2 error
	for range 10 {
		var v int
		if err2 = s.Recv(&v); err2 != nil {
			break
		}
	}
	var we *Error
	if !errors.As(err2, &we) || we.Code != CodeUnavailable {
		t.Fatalf("want overflow error, got %v", err2)
	}
}

func TestEventsAndServerCalls(t *testing.T) {
	got := make(chan string, 10)
	m := NewMux()
	m.OnEvent("ping", func(c *Conn, name string, body Raw) {
		var s string
		Unmarshal(body, &s)
		got <- s
		// Calls go both ways: the server calls the client back.
		go func() {
			var out string
			c.Call(context.Background(), "whoami", nil, &out)
			got <- out
		}()
	})
	path := sockPath(t)
	ln, err := Listen(path, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go (&Server{Mux: m, Hello: testHello}).Serve(ln)
	cm := NewMux()
	cm.Handle("whoami", func(context.Context, *Conn, Raw) (any, error) { return "client", nil })
	c, err := Dial(context.Background(), path, cm, testHello, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Notify("ping", "hello")
	for _, want := range []string{"hello", "client"} {
		select {
		case s := <-got:
			if s != want {
				t.Fatalf("got %q, want %q", s, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for %q", want)
		}
	}
}

func TestReconnect(t *testing.T) {
	path := sockPath(t)
	start := func() net.Listener {
		ln, err := Listen(path, 0, -1)
		if err != nil {
			t.Fatal(err)
		}
		go (&Server{Mux: echoMux(), Hello: testHello}).Serve(ln)
		return ln
	}
	ln := start()
	var connects, disconnects atomic.Int32
	cl := &Client{
		Path: path, Hello: testHello,
		MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
		OnConnect:    func(*Conn) { connects.Add(1) },
		OnDisconnect: func(*Conn, error) { disconnects.Add(1) },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.Run(ctx)
	wctx, wcancel := context.WithTimeout(ctx, 2*time.Second)
	defer wcancel()
	if _, err := cl.Wait(wctx); err != nil {
		t.Fatal(err)
	}
	// Server restarts: close listener and existing connections.
	ln.Close()
	cl.Conn().Close()
	if err := cl.Call(ctx, "echo", echoIn{}, nil); err == nil {
		// Might already be reconnecting; that is fine.
		_ = err
	}
	time.Sleep(50 * time.Millisecond)
	ln = start()
	defer ln.Close()
	deadline := time.Now().Add(3 * time.Second)
	for connects.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("did not reconnect: connects=%d", connects.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if disconnects.Load() < 1 {
		t.Fatal("OnDisconnect not called")
	}
	c, _ := cl.Wait(wctx)
	var out echoIn
	if err := c.Call(ctx, "echo", echoIn{N: 5}, &out); err != nil || out.N != 5 {
		t.Fatalf("call after reconnect: %v", err)
	}
}

func TestVersions(t *testing.T) {
	cases := []struct {
		name        string
		srv, cli    Hello
		wantErr     bool
		wantVersion int
		wantCanary  bool
	}{
		{"same", testHello, testHello, false, 2, true},
		{"older client", testHello, Hello{Proto: "test", Version: 1, MinVersion: 1}, false, 1, false},
		{"client too old", Hello{Proto: "test", Version: 3, MinVersion: 2}, Hello{Proto: "test", Version: 1, MinVersion: 1}, true, 0, false},
		{"server too old", Hello{Proto: "test", Version: 1, MinVersion: 1}, Hello{Proto: "test", Version: 3, MinVersion: 3}, true, 0, false},
		{"other proto", testHello, Hello{Proto: "other", Version: 2}, true, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := sockPath(t)
			ln, err := Listen(path, 0, -1)
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go (&Server{Mux: echoMux(), Hello: tc.srv}).Serve(ln)
			c, err := Dial(context.Background(), path, nil, tc.cli, nil)
			if tc.wantErr {
				if err == nil {
					c.Close()
					t.Fatal("want handshake error")
				}
				var we *Error
				if !errors.As(err, &we) || we.Code != "incompatible" {
					t.Fatalf("want incompatible error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if c.Version() != tc.wantVersion {
				t.Errorf("version = %d", c.Version())
			}
			if c.Has("canary") != tc.wantCanary {
				t.Errorf("Has(canary) = %v", c.Has("canary"))
			}
			if err := c.Call(context.Background(), "echo", echoIn{}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSocketMode(t *testing.T) {
	path := sockPath(t)
	ln, err := Listen(path, 0o660, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, err := Listen(path, 0, -1); err == nil {
		t.Fatal("second listener on a live socket must fail")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.PathSeparator == '/' && fi.Mode().Perm() != 0o660 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
}

func TestSeveralProtocolsOnOneSocket(t *testing.T) {
	path := sockPath(t)
	ln, err := Listen(path, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &Server{Mux: echoMux(), Hello: Hello{Proto: "a", Version: 1}, Also: []Hello{{Proto: "b", Version: 3}}}
	go srv.Serve(ln)
	for proto, version := range map[string]int{"a": 1, "b": 3} {
		c, err := Dial(context.Background(), path, nil, Hello{Proto: proto, Version: 5}, nil)
		if err != nil {
			t.Fatalf("%s: %v", proto, err)
		}
		if c.Proto() != proto || c.Version() != version {
			t.Errorf("%s: proto %s version %d", proto, c.Proto(), c.Version())
		}
		c.Close()
	}
	if _, err := Dial(context.Background(), path, nil, Hello{Proto: "c"}, nil); err == nil {
		t.Fatal("unknown protocol must fail")
	}
}

func TestTimeKeepsUTC(t *testing.T) {
	in := time.Date(2026, 10, 31, 12, 0, 0, 123456789, time.UTC)
	b, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out time.Time
	if err := Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Equal(in) || out.Location() != time.UTC {
		t.Fatalf("got %v (%v)", out, out.Location())
	}
}
