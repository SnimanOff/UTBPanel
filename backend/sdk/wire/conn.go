package wire

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"
)

// ErrNotConnected is returned by Client calls while there is no connection.
var ErrNotConnected = errors.New("wire: not connected")

// Listen opens a unix socket at path, replacing a stale socket file.
// The socket gets mode perm (0660 when 0) and, if gid >= 0, that group, so
// containers sharing the group can connect.
func Listen(path string, perm os.FileMode, gid int) (net.Listener, error) {
	if perm == 0 {
		perm = 0o660
	}
	if _, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
			c.Close()
			return nil, errors.New("wire: socket " + path + " is in use")
		}
		os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, perm); err != nil {
		ln.Close()
		return nil, err
	}
	if gid >= 0 {
		if err := os.Chown(path, -1, gid); err != nil {
			ln.Close()
			return nil, err
		}
	}
	return ln, nil
}

// Server accepts connections and runs the handshake for each.
type Server struct {
	Mux   *Mux
	Hello Hello
	// Also lists more protocols served on the same socket.
	Also         []Hello
	Log          *slog.Logger
	OnConnect    func(*Conn)
	OnDisconnect func(*Conn, error)

	mu    sync.Mutex
	conns map[*Conn]struct{}
}

// Serve accepts connections until ln is closed.
func (s *Server) Serve(ln net.Listener) error {
	for {
		nc, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(nc)
	}
}

func (s *Server) handle(nc net.Conn) {
	c := newConn(nc, s.Mux, s.Log)
	if err := c.serverHandshake(append([]Hello{s.Hello}, s.Also...)); err != nil {
		c.log.Warn("wire handshake failed", "proto", s.Hello.Proto, "err", err)
		c.shutdown(err)
		return
	}
	s.mu.Lock()
	if s.conns == nil {
		s.conns = map[*Conn]struct{}{}
	}
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	if s.OnConnect != nil {
		s.OnConnect(c)
	}
	err := c.readLoop()
	c.shutdown(err)
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	if s.OnDisconnect != nil {
		s.OnDisconnect(c, err)
	}
}

// Conns returns the current connections.
func (s *Server) Conns() []*Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Conn, 0, len(s.conns))
	for c := range s.conns {
		out = append(out, c)
	}
	return out
}

// Broadcast pushes an event to every connection.
func (s *Server) Broadcast(name string, body any) {
	for _, c := range s.Conns() {
		c.Notify(name, body)
	}
}

// Dial connects once and runs the handshake. The returned connection reads
// frames in the background until it is closed.
func Dial(ctx context.Context, path string, mux *Mux, hello Hello, log *slog.Logger) (*Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	c := newConn(nc, mux, log)
	if dl, ok := ctx.Deadline(); ok {
		nc.SetDeadline(dl)
	}
	if err := c.clientHandshake(hello); err != nil {
		c.shutdown(err)
		return nil, err
	}
	nc.SetDeadline(time.Time{})
	go func() { c.shutdown(c.readLoop()) }()
	return c, nil
}

// Client keeps one connection open, reconnecting with a pause that grows
// from MinBackoff (0.5 s) to MaxBackoff (10 s).
type Client struct {
	Path         string
	Mux          *Mux
	Hello        Hello
	Log          *slog.Logger
	OnConnect    func(*Conn)
	OnDisconnect func(*Conn, error)
	MinBackoff   time.Duration
	MaxBackoff   time.Duration

	mu      sync.Mutex
	conn    *Conn
	changed chan struct{}
}

// Run connects and reconnects until ctx is done.
func (cl *Client) Run(ctx context.Context) {
	minB, maxB := cl.MinBackoff, cl.MaxBackoff
	if minB <= 0 {
		minB = 500 * time.Millisecond
	}
	if maxB <= 0 {
		maxB = 10 * time.Second
	}
	log := cl.Log
	if log == nil {
		log = slog.Default()
	}
	backoff := minB
	for ctx.Err() == nil {
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		c, err := Dial(dctx, cl.Path, cl.Mux, cl.Hello, log)
		cancel()
		if err != nil {
			var we *Error
			if errors.As(err, &we) {
				log.Error("wire: incompatible peer", "path", cl.Path, "err", err)
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff = min(backoff*2, maxB)
			continue
		}
		backoff = minB
		cl.set(c)
		if cl.OnConnect != nil {
			cl.OnConnect(c)
		}
		select {
		case <-c.Done():
		case <-ctx.Done():
			c.Close()
		}
		cl.set(nil)
		if cl.OnDisconnect != nil {
			cl.OnDisconnect(c, c.Err())
		}
	}
}

func (cl *Client) set(c *Conn) {
	cl.mu.Lock()
	cl.conn = c
	if cl.changed != nil {
		close(cl.changed)
		cl.changed = nil
	}
	cl.mu.Unlock()
}

// Conn returns the current connection or nil.
func (cl *Client) Conn() *Conn {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.conn
}

// Wait blocks until connected or ctx is done.
func (cl *Client) Wait(ctx context.Context) (*Conn, error) {
	for {
		cl.mu.Lock()
		if cl.conn != nil {
			c := cl.conn
			cl.mu.Unlock()
			return c, nil
		}
		if cl.changed == nil {
			cl.changed = make(chan struct{})
		}
		ch := cl.changed
		cl.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Call calls the peer over the current connection.
func (cl *Client) Call(ctx context.Context, method string, in, out any) error {
	c := cl.Conn()
	if c == nil {
		return ErrNotConnected
	}
	return c.Call(ctx, method, in, out)
}

// Notify pushes an event over the current connection.
func (cl *Client) Notify(name string, body any) error {
	c := cl.Conn()
	if c == nil {
		return ErrNotConnected
	}
	return c.Notify(name, body)
}
