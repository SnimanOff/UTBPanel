// Package wire is the transport shared by all internal protocols:
// edge ↔ core, core ↔ panel, SDK ↔ core and the DB driver ↔ core.
//
// A connection carries frames over a unix socket:
//
//	length uint32 | type uint8 | id uint32 | payload (CBOR)
//
// Both sides can call each other (request/response with deadlines taken from
// context), push events, and open streams with a bounded buffer.
package wire

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// MaxFrame is the largest payload of one frame. Bigger data goes in streams.
const MaxFrame = 16 << 20

const (
	fHello uint8 = iota + 1
	fRequest
	fResponse
	fEvent
	fCancel
	fStreamData
	fStreamEnd
)

// Raw is an encoded CBOR value.
type Raw = cbor.RawMessage

var (
	encMode, _ = func() (cbor.EncMode, error) {
		o := cbor.CoreDetEncOptions()
		o.Time = cbor.TimeRFC3339Nano // keeps UTC and sub-second precision
		return o.EncMode()
	}()
	decMode, _ = cbor.DecOptions{MaxArrayElements: 1 << 20, MaxMapPairs: 1 << 20}.DecMode()
)

// Marshal encodes v with the wire codec.
func Marshal(v any) (Raw, error) {
	if v == nil {
		return nil, nil
	}
	return encMode.Marshal(v)
}

// Unmarshal decodes data into v. Empty data leaves v untouched.
func Unmarshal(data Raw, v any) error {
	if len(data) == 0 || v == nil {
		return nil
	}
	return decMode.Unmarshal(data, v)
}

// Error is an error returned by the remote side.
type Error struct {
	Code    string `cbor:"1,keyasint"`
	Message string `cbor:"2,keyasint"`
	Data    Raw    `cbor:"3,keyasint,omitempty"`
}

func (e *Error) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// Errorf makes an *Error with a code.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Common error codes.
const (
	CodeNotFound    = "not_found"
	CodeNoMethod    = "no_method"
	CodeBadRequest  = "bad_request"
	CodeInternal    = "internal"
	CodeCanceled    = "canceled"
	CodeUnavailable = "unavailable"
)

// ErrClosed is returned for calls on a closed connection.
var ErrClosed = errors.New("wire: connection closed")

func toError(err error) *Error {
	if err == nil {
		return nil
	}
	var we *Error
	if errors.As(err, &we) {
		return we
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeCanceled, Message: err.Error()}
	}
	return &Error{Code: CodeInternal, Message: err.Error()}
}

type request struct {
	Method   string `cbor:"1,keyasint"`
	Deadline int64  `cbor:"2,keyasint,omitempty"` // unix nanoseconds
	Stream   bool   `cbor:"3,keyasint,omitempty"`
	Body     Raw    `cbor:"4,keyasint,omitempty"`
}

type response struct {
	Err  *Error `cbor:"1,keyasint,omitempty"`
	Body Raw    `cbor:"2,keyasint,omitempty"`
}

type event struct {
	Name string `cbor:"1,keyasint"`
	Body Raw    `cbor:"2,keyasint,omitempty"`
}

// Handler serves one call. The returned value is encoded as the response.
type Handler func(ctx context.Context, c *Conn, body Raw) (any, error)

// StreamHandler serves a stream: it sends values until it returns.
type StreamHandler func(ctx context.Context, c *Conn, body Raw, s *Sender) error

// EventHandler receives pushed events.
type EventHandler func(c *Conn, name string, body Raw)

// Mux routes incoming calls, streams and events by name.
type Mux struct {
	mu      sync.RWMutex
	calls   map[string]Handler
	streams map[string]StreamHandler
	events  map[string]EventHandler
	anyEvt  EventHandler
}

// NewMux returns an empty router.
func NewMux() *Mux {
	return &Mux{calls: map[string]Handler{}, streams: map[string]StreamHandler{}, events: map[string]EventHandler{}}
}

// Handle registers a call handler.
func (m *Mux) Handle(method string, h Handler) {
	m.mu.Lock()
	m.calls[method] = h
	m.mu.Unlock()
}

// HandleStream registers a stream handler.
func (m *Mux) HandleStream(method string, h StreamHandler) {
	m.mu.Lock()
	m.streams[method] = h
	m.mu.Unlock()
}

// OnEvent registers an event handler; name "*" receives all unhandled events.
func (m *Mux) OnEvent(name string, h EventHandler) {
	m.mu.Lock()
	if name == "*" {
		m.anyEvt = h
	} else {
		m.events[name] = h
	}
	m.mu.Unlock()
}

// Func adapts a typed function to a Handler.
func Func[In, Out any](f func(ctx context.Context, c *Conn, in In) (Out, error)) Handler {
	return func(ctx context.Context, c *Conn, body Raw) (any, error) {
		var in In
		if err := Unmarshal(body, &in); err != nil {
			return nil, &Error{Code: CodeBadRequest, Message: err.Error()}
		}
		return f(ctx, c, in)
	}
}

// Conn is one connection. It is safe for concurrent use.
type Conn struct {
	nc   net.Conn
	mux  *Mux
	log  *slog.Logger
	info PeerInfo
	self Hello

	wmu sync.Mutex
	bw  *bufio.Writer

	mu       sync.Mutex
	nextID   uint32
	pending  map[uint32]chan response
	streams  map[uint32]*Stream
	inflight map[uint32]context.CancelFunc

	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error
	version   atomic.Int32

	// Values attached by the application, e.g. the bot id of an SDK peer.
	Values sync.Map
}

// PeerInfo describes the remote side after the handshake.
type PeerInfo struct {
	Hello   Hello
	Version int // negotiated
}

func newConn(nc net.Conn, mux *Mux, log *slog.Logger) *Conn {
	if mux == nil {
		mux = NewMux()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Conn{
		nc: nc, mux: mux, log: log,
		bw:       bufio.NewWriterSize(nc, 64<<10),
		pending:  map[uint32]chan response{},
		streams:  map[uint32]*Stream{},
		inflight: map[uint32]context.CancelFunc{},
		closed:   make(chan struct{}),
	}
}

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.closed }

// Err returns why the connection ended.
func (c *Conn) Err() error {
	select {
	case <-c.closed:
		return c.closeErr
	default:
		return nil
	}
}

// Close ends the connection.
func (c *Conn) Close() error {
	c.shutdown(ErrClosed)
	return nil
}

func (c *Conn) shutdown(err error) {
	c.closeOnce.Do(func() {
		c.closeErr = err
		c.nc.Close()
		close(c.closed)
		c.mu.Lock()
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		for id, s := range c.streams {
			s.finish(&Error{Code: CodeUnavailable, Message: "connection closed"})
			delete(c.streams, id)
		}
		for _, cancel := range c.inflight {
			cancel()
		}
		c.mu.Unlock()
	})
}

// RemoteAddr returns the peer address.
func (c *Conn) RemoteAddr() net.Addr { return c.nc.RemoteAddr() }

// NetConn returns the underlying connection (for SO_PEERCRED checks).
func (c *Conn) NetConn() net.Conn { return c.nc }

func (c *Conn) writeFrame(typ uint8, id uint32, payload []byte) error {
	if len(payload) > MaxFrame {
		return fmt.Errorf("wire: frame of %d bytes exceeds %d", len(payload), MaxFrame)
	}
	var hdr [9]byte
	binary.BigEndian.PutUint32(hdr[0:], uint32(len(payload)))
	hdr[4] = typ
	binary.BigEndian.PutUint32(hdr[5:], id)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	select {
	case <-c.closed:
		return ErrClosed
	default:
	}
	if _, err := c.bw.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := c.bw.Write(payload); err != nil {
		return err
	}
	return c.bw.Flush()
}

func (c *Conn) send(typ uint8, id uint32, v any) error {
	b, err := Marshal(v)
	if err != nil {
		return err
	}
	return c.writeFrame(typ, id, b)
}

func readFrame(r io.Reader) (uint8, uint32, []byte, error) {
	var hdr [9]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[0:])
	if n > MaxFrame {
		return 0, 0, nil, fmt.Errorf("wire: frame of %d bytes exceeds %d", n, MaxFrame)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, 0, nil, err
	}
	return hdr[4], binary.BigEndian.Uint32(hdr[5:]), buf, nil
}

func (c *Conn) newID() uint32 {
	c.nextID++
	if c.nextID == 0 {
		c.nextID = 1
	}
	return c.nextID
}

// Call sends a request and decodes the response into out (may be nil).
func (c *Conn) Call(ctx context.Context, method string, in, out any) error {
	body, err := Marshal(in)
	if err != nil {
		return err
	}
	req := request{Method: method, Body: body}
	if d, ok := ctx.Deadline(); ok {
		req.Deadline = d.UnixNano()
	}
	ch := make(chan response, 1)
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return ErrClosed
	default:
	}
	id := c.newID()
	c.pending[id] = ch
	c.mu.Unlock()
	cleanup := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}
	if err := c.send(fRequest, id, req); err != nil {
		cleanup()
		return err
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if resp.Err != nil {
			return resp.Err
		}
		return Unmarshal(resp.Body, out)
	case <-ctx.Done():
		cleanup()
		c.writeFrame(fCancel, id, nil)
		return ctx.Err()
	case <-c.closed:
		return ErrClosed
	}
}

// Notify pushes an event to the peer.
func (c *Conn) Notify(name string, body any) error {
	b, err := Marshal(body)
	if err != nil {
		return err
	}
	return c.send(fEvent, 0, event{Name: name, Body: b})
}

// StreamOptions control the receive buffer of a stream.
type StreamOptions struct {
	Buffer int  // frames kept on the receiving side; default 256
	Lossy  bool // drop the oldest frame when full (live metrics) instead of failing
}

// Stream receives values from a remote StreamHandler.
type Stream struct {
	c     *Conn
	id    uint32
	opts  StreamOptions
	mu    sync.Mutex
	buf   []Raw
	ready chan struct{}
	err   error
	done  bool
	drops atomic.Int64
}

// OpenStream starts a remote stream.
func (c *Conn) OpenStream(ctx context.Context, method string, in any, opts StreamOptions) (*Stream, error) {
	if opts.Buffer <= 0 {
		opts.Buffer = 256
	}
	body, err := Marshal(in)
	if err != nil {
		return nil, err
	}
	s := &Stream{c: c, opts: opts, ready: make(chan struct{}, 1)}
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return nil, ErrClosed
	default:
	}
	s.id = c.newID()
	c.streams[s.id] = s
	c.mu.Unlock()
	req := request{Method: method, Stream: true, Body: body}
	if err := c.send(fRequest, s.id, req); err != nil {
		c.dropStream(s.id)
		return nil, err
	}
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-c.closed:
		}
	}()
	return s, nil
}

func (c *Conn) dropStream(id uint32) {
	c.mu.Lock()
	delete(c.streams, id)
	c.mu.Unlock()
}

func (s *Stream) push(b Raw) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	if len(s.buf) >= s.opts.Buffer {
		if !s.opts.Lossy {
			s.mu.Unlock()
			s.c.writeFrame(fCancel, s.id, nil)
			s.c.dropStream(s.id)
			s.finish(&Error{Code: CodeUnavailable, Message: "stream buffer overflow: reader too slow"})
			return
		}
		s.buf = s.buf[1:]
		s.drops.Add(1)
	}
	s.buf = append(s.buf, b)
	s.mu.Unlock()
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

func (s *Stream) finish(err *Error) {
	s.mu.Lock()
	if !s.done {
		s.done = true
		if err != nil {
			s.err = err
		} else {
			s.err = io.EOF
		}
	}
	s.mu.Unlock()
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// Recv decodes the next value into out. It returns io.EOF at the end.
func (s *Stream) Recv(out any) error {
	for {
		s.mu.Lock()
		if len(s.buf) > 0 {
			b := s.buf[0]
			s.buf = s.buf[1:]
			s.mu.Unlock()
			return Unmarshal(b, out)
		}
		if s.done {
			err := s.err
			s.mu.Unlock()
			return err
		}
		s.mu.Unlock()
		<-s.ready
	}
}

// Dropped returns how many frames a lossy stream discarded.
func (s *Stream) Dropped() int64 { return s.drops.Load() }

// Close stops the stream.
func (s *Stream) Close() {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done {
		return
	}
	s.c.writeFrame(fCancel, s.id, nil)
	s.c.dropStream(s.id)
	s.finish(&Error{Code: CodeCanceled, Message: "stream closed"})
}

// Sender is the sending side of a stream.
type Sender struct {
	c  *Conn
	id uint32
}

// Send writes one value to the stream.
func (s *Sender) Send(v any) error {
	return s.c.send(fStreamData, s.id, v)
}

func (c *Conn) readLoop() error {
	r := bufio.NewReaderSize(c.nc, 64<<10)
	for {
		typ, id, payload, err := readFrame(r)
		if err != nil {
			return err
		}
		switch typ {
		case fRequest:
			var req request
			if err := Unmarshal(payload, &req); err != nil {
				return fmt.Errorf("wire: bad request: %w", err)
			}
			c.serve(id, req)
		case fResponse:
			var resp response
			if err := Unmarshal(payload, &resp); err != nil {
				return fmt.Errorf("wire: bad response: %w", err)
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- resp
			}
		case fEvent:
			var ev event
			if err := Unmarshal(payload, &ev); err != nil {
				return fmt.Errorf("wire: bad event: %w", err)
			}
			c.dispatchEvent(ev)
		case fCancel:
			c.mu.Lock()
			cancel := c.inflight[id]
			c.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		case fStreamData:
			c.mu.Lock()
			s := c.streams[id]
			c.mu.Unlock()
			if s != nil {
				s.push(Raw(payload))
			}
		case fStreamEnd:
			var resp response
			if err := Unmarshal(payload, &resp); err != nil {
				return fmt.Errorf("wire: bad stream end: %w", err)
			}
			c.mu.Lock()
			s := c.streams[id]
			delete(c.streams, id)
			c.mu.Unlock()
			if s != nil {
				s.finish(resp.Err)
			}
		case fHello:
			// Only valid during the handshake.
			return errors.New("wire: unexpected hello")
		default:
			return fmt.Errorf("wire: unknown frame type %d", typ)
		}
	}
}

func (c *Conn) dispatchEvent(ev event) {
	c.mux.mu.RLock()
	h := c.mux.events[ev.Name]
	if h == nil {
		h = c.mux.anyEvt
	}
	c.mux.mu.RUnlock()
	if h == nil {
		return
	}
	// Events keep their order: handlers run on the read goroutine and must
	// be quick.
	func() {
		defer c.recoverPanic("event " + ev.Name)
		h(c, ev.Name, ev.Body)
	}()
}

func (c *Conn) recoverPanic(what string) {
	if p := recover(); p != nil {
		c.log.Error("panic in wire handler", "handler", what, "panic", p, "stack", string(debug.Stack()))
	}
}

func (c *Conn) serve(id uint32, req request) {
	ctx, cancel := context.WithCancel(context.Background())
	if req.Deadline != 0 {
		ctx, cancel = context.WithDeadline(context.Background(), time.Unix(0, req.Deadline))
	}
	c.mu.Lock()
	c.inflight[id] = cancel
	c.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			c.mu.Lock()
			delete(c.inflight, id)
			c.mu.Unlock()
		}()
		if req.Stream {
			c.serveStream(ctx, id, req)
			return
		}
		var resp response
		func() {
			defer func() {
				if p := recover(); p != nil {
					c.log.Error("panic in wire handler", "method", req.Method, "panic", p, "stack", string(debug.Stack()))
					resp = response{Err: &Error{Code: CodeInternal, Message: fmt.Sprint("panic: ", p)}}
				}
			}()
			c.mux.mu.RLock()
			h := c.mux.calls[req.Method]
			c.mux.mu.RUnlock()
			if h == nil {
				resp.Err = Errorf(CodeNoMethod, "unknown method %q", req.Method)
				return
			}
			out, err := h(ctx, c, req.Body)
			if err != nil {
				resp.Err = toError(err)
				return
			}
			b, err := Marshal(out)
			if err != nil {
				resp.Err = toError(err)
				return
			}
			resp.Body = b
		}()
		if err := c.send(fResponse, id, resp); err != nil && !errors.Is(err, ErrClosed) {
			// A response too big for one frame: tell the caller instead.
			c.send(fResponse, id, response{Err: toError(err)})
		}
	}()
}

func (c *Conn) serveStream(ctx context.Context, id uint32, req request) {
	var end response
	func() {
		defer func() {
			if p := recover(); p != nil {
				c.log.Error("panic in wire stream", "method", req.Method, "panic", p, "stack", string(debug.Stack()))
				end.Err = &Error{Code: CodeInternal, Message: fmt.Sprint("panic: ", p)}
			}
		}()
		c.mux.mu.RLock()
		h := c.mux.streams[req.Method]
		c.mux.mu.RUnlock()
		if h == nil {
			end.Err = Errorf(CodeNoMethod, "unknown stream %q", req.Method)
			return
		}
		end.Err = toError(h(ctx, c, req.Body, &Sender{c: c, id: id}))
	}()
	c.send(fStreamEnd, id, end)
}
