package counters

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// LiveBot is one bot in one second.
type LiveBot struct {
	UPS      float64 `cbor:"1,keyasint" json:"ups"`
	InFlight int64   `cbor:"2,keyasint" json:"inflight"`
	RespMS   float64 `cbor:"3,keyasint" json:"resp_ms"` // average over the last 10 s
	Dropped  float64 `cbor:"4,keyasint" json:"dropped"`
	CPU      float64 `cbor:"5,keyasint" json:"cpu"`
	RSS      int64   `cbor:"6,keyasint" json:"rss"`
	APIPS    float64 `cbor:"7,keyasint" json:"api_ps,omitempty"`
	DBPS     float64 `cbor:"8,keyasint" json:"db_ps,omitempty"`
}

// LiveServer is the panel-wide part.
type LiveServer struct {
	CPU  float64 `cbor:"1,keyasint" json:"cpu"`
	RAM  float64 `cbor:"2,keyasint" json:"ram"`
	Disk float64 `cbor:"3,keyasint" json:"disk"`
	RX   float64 `cbor:"4,keyasint" json:"rx"`
	TX   float64 `cbor:"5,keyasint" json:"tx"`
	UPS  float64 `cbor:"6,keyasint" json:"ups"`
}

// Snapshot is one second of live metrics.
type Snapshot struct {
	TS     int64             `cbor:"1,keyasint" json:"ts"`
	Server LiveServer        `cbor:"2,keyasint" json:"server"`
	Bots   map[int64]LiveBot `cbor:"3,keyasint" json:"bots"`
}

type second struct {
	updates, dropped, api int64
	latSum                time.Duration
	latN                  int64
}

// Live keeps per-second counters and the last 5 minutes of snapshots.
type Live struct {
	// InFlight, Usage and Host fill the snapshot from other parts.
	InFlight func(botID int64) int64
	Usage    func(botID int64) (cpu float64, rss int64)
	Host     func() Server
	Bots     func() []int64

	mu      sync.Mutex
	cur     map[int64]*second
	lat     map[int64][]second // last 10 seconds per bot, for the average
	ring    []Snapshot
	subs    map[chan Snapshot]struct{}
	running bool
}

// HistorySeconds is how much live history is kept.
const HistorySeconds = 300

// Observe counts an update in the current second.
func (l *Live) Observe(o ingress.Outcome) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur == nil {
		l.cur = map[int64]*second{}
	}
	s := l.cur[o.Update.BotID]
	if s == nil {
		s = &second{}
		l.cur[o.Update.BotID] = s
	}
	s.updates++
	if strings.HasPrefix(o.Result, "dropped_") {
		s.dropped++
	}
	if o.Latency > 0 {
		s.latSum += o.Latency
		s.latN++
	}
}

// APICall counts a Bot API call in the current second.
func (l *Live) APICall(botID int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur == nil {
		l.cur = map[int64]*second{}
	}
	s := l.cur[botID]
	if s == nil {
		s = &second{}
		l.cur[botID] = s
	}
	s.api++
}

// tick closes the second and builds the snapshot.
func (l *Live) tick(now time.Time) Snapshot {
	l.mu.Lock()
	cur := l.cur
	l.cur = map[int64]*second{}
	if l.lat == nil {
		l.lat = map[int64][]second{}
	}
	l.mu.Unlock()

	snap := Snapshot{TS: now.Unix(), Bots: map[int64]LiveBot{}}
	var ids []int64
	if l.Bots != nil {
		ids = l.Bots()
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	for id := range cur {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		s := cur[id]
		if s == nil {
			s = &second{}
		}
		b := LiveBot{UPS: float64(s.updates), Dropped: float64(s.dropped), APIPS: float64(s.api)}
		l.mu.Lock()
		win := append(l.lat[id], *s)
		if len(win) > 10 {
			win = win[len(win)-10:]
		}
		l.lat[id] = win
		l.mu.Unlock()
		var sum time.Duration
		var n int64
		for _, w := range win {
			sum += w.latSum
			n += w.latN
		}
		if n > 0 {
			b.RespMS = float64(sum) / float64(n) / float64(time.Millisecond)
		}
		if l.InFlight != nil {
			b.InFlight = l.InFlight(id)
		}
		if l.Usage != nil {
			b.CPU, b.RSS = l.Usage(id)
		}
		snap.Server.UPS += b.UPS
		snap.Bots[id] = b
	}
	if l.Host != nil {
		h := l.Host()
		snap.Server.CPU, snap.Server.RAM, snap.Server.Disk = h.CPU, h.RAMPercent(), h.DiskPercent()
		snap.Server.RX, snap.Server.TX = h.NetRx, h.NetTx
	}
	l.mu.Lock()
	l.ring = append(l.ring, snap)
	if len(l.ring) > HistorySeconds {
		l.ring = l.ring[len(l.ring)-HistorySeconds:]
	}
	for ch := range l.subs {
		select {
		case ch <- snap:
		default: // a slow reader misses a second, never blocks the core
		}
	}
	l.mu.Unlock()
	return snap
}

// Run ticks every second until ctx ends.
func (l *Live) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			l.tick(now)
		}
	}
}

// History returns the last 5 minutes.
func (l *Live) History() []Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Snapshot(nil), l.ring...)
}

func (l *Live) subscribe() chan Snapshot {
	ch := make(chan Snapshot, 8)
	l.mu.Lock()
	if l.subs == nil {
		l.subs = map[chan Snapshot]struct{}{}
	}
	l.subs[ch] = struct{}{}
	l.mu.Unlock()
	return ch
}

func (l *Live) unsubscribe(ch chan Snapshot) {
	l.mu.Lock()
	delete(l.subs, ch)
	l.mu.Unlock()
}

// Socket methods.
const (
	MethodLive        = "live.follow"
	MethodLiveHistory = "live.history"
)

// Register adds the live stream to the core socket.
func (l *Live) Register(mux *wire.Mux) {
	mux.Handle(MethodLiveHistory, wire.Func(func(context.Context, *wire.Conn, struct{}) ([]Snapshot, error) {
		return l.History(), nil
	}))
	mux.HandleStream(MethodLive, func(ctx context.Context, _ *wire.Conn, _ wire.Raw, s *wire.Sender) error {
		ch := l.subscribe()
		defer l.unsubscribe(ch)
		for {
			select {
			case <-ctx.Done():
				return nil
			case snap := <-ch:
				if err := s.Send(snap); err != nil {
					return err
				}
			}
		}
	})
}
