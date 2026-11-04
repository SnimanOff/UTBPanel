package livehub

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/counters"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Package livehub is the panel side of live metrics.
//
// LiveHub holds one stream from the core and fans it out to every open
// page: one SSE stream per interface, filtered by bots on request.
type LiveHub struct {
	Core *wire.Client
	Log  *slog.Logger

	mu      sync.Mutex
	clients map[chan counters.Snapshot]struct{}
}

// Run keeps the core stream open while ctx lives.
func (h *LiveHub) Run(ctx context.Context) {
	for ctx.Err() == nil {
		c, err := h.Core.Wait(ctx)
		if err != nil {
			return
		}
		s, err := c.OpenStream(ctx, counters.MethodLive, nil, wire.StreamOptions{Buffer: 16, Lossy: true})
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		for {
			var snap counters.Snapshot
			if err := s.Recv(&snap); err != nil {
				break
			}
			h.mu.Lock()
			for ch := range h.clients {
				select {
				case ch <- snap:
				default:
				}
			}
			h.mu.Unlock()
		}
		s.Close()
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// Register adds GET /api/live.
func (h *LiveHub) Register(mux *api.Mux) {
	mux.HandleFunc("GET /api/live", h.serve)
}

func (h *LiveHub) serve(w http.ResponseWriter, r *http.Request) {
	var only map[int64]bool
	if v := r.URL.Query().Get("bots"); v != "" {
		only = map[int64]bool{}
		for _, s := range strings.Split(v, ",") {
			if id, err := strconv.ParseInt(s, 10, 64); err == nil {
				only[id] = true
			}
		}
	}
	caller := api.CallerFrom(r.Context())
	limited := caller.Scope != nil && !caller.Scope.All
	filter := func(s counters.Snapshot) counters.Snapshot {
		if only == nil && !limited {
			return s
		}
		out := counters.Snapshot{TS: s.TS, Server: s.Server, Bots: map[int64]counters.LiveBot{}}
		if limited {
			// Someone with access to some bots does not see the server.
			out.Server = counters.Snapshot{}.Server
		}
		for id, b := range s.Bots {
			if (only == nil || only[id]) && caller.SeesBot(id) {
				out.Bots[id] = b
			}
		}
		return out
	}
	sse := api.NewSSE(w)
	// The last 5 minutes first: the "Live" chart starts full.
	if r.URL.Query().Get("history") == "1" {
		var hist []counters.Snapshot
		if h.Core.Call(r.Context(), counters.MethodLiveHistory, struct{}{}, &hist) == nil {
			for i := range hist {
				hist[i] = filter(hist[i])
			}
			sse.Send("", "history", hist)
		}
	}
	ch := make(chan counters.Snapshot, 4)
	h.mu.Lock()
	if h.clients == nil {
		h.clients = map[chan counters.Snapshot]struct{}{}
	}
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
	}()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case s := <-ch:
			if sse.Send(strconv.FormatInt(s.TS, 10), "live", filter(s)) != nil {
				return
			}
		case <-ping.C:
			if sse.Ping() != nil {
				return
			}
		}
	}
}
