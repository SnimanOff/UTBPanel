package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SSE writes Server-Sent Events.
type SSE struct {
	w    http.ResponseWriter
	f    http.Flusher
	last time.Time
}

// NewSSE sends the event-stream headers.
func NewSSE(w http.ResponseWriter) *SSE {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	s := &SSE{w: w, f: f}
	fmt.Fprint(w, "retry: 2000\n\n")
	s.flush()
	return s
}

func (s *SSE) flush() {
	if s.f != nil {
		s.f.Flush()
	}
}

// Send writes one event with an id (for Last-Event-ID) and a JSON body.
func (s *SSE) Send(id, event string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if id != "" {
		fmt.Fprintf(s.w, "id: %s\n", id)
	}
	if event != "" {
		fmt.Fprintf(s.w, "event: %s\n", event)
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return err
	}
	s.flush()
	s.last = time.Now()
	return nil
}

// Ping keeps idle connections open through proxies.
func (s *SSE) Ping() error {
	if _, err := fmt.Fprint(s.w, ": ping\n\n"); err != nil {
		return err
	}
	s.flush()
	return nil
}
