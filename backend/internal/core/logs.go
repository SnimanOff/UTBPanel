package core

import (
	"context"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/logs"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// LogsQuery asks for lines of a bot.
type LogsQuery struct {
	BotID  int64       `cbor:"1,keyasint"`
	Filter logs.Filter `cbor:"2,keyasint"`
	Limit  int         `cbor:"3,keyasint,omitempty"`
	From   time.Time   `cbor:"4,keyasint,omitzero"`
	To     time.Time   `cbor:"5,keyasint,omitzero"`
	// Backlog: lines from memory sent before live ones (follow).
	Backlog int `cbor:"6,keyasint,omitempty"`
}

func (c *Core) newLogs() *logs.Store {
	store := logs.New(func(id int64) string {
		return filepath.Join(c.Cfg.DataDir, "bots", strconv.FormatInt(id, 10), "logs")
	}, c.Log)
	store.Redact = c.Sec.Redact.Redact
	return store
}

// chunkWriter sends written bytes as stream frames.
type chunkWriter struct{ s *wire.Sender }

func (w chunkWriter) Write(p []byte) (int, error) {
	for off := 0; off < len(p); off += 256 << 10 {
		end := min(off+256<<10, len(p))
		if err := w.s.Send(p[off:end]); err != nil {
			return off, err
		}
	}
	return len(p), nil
}

func (c *Core) registerLogs() {
	c.Mux.Handle("logs.tail", wire.Func(func(_ context.Context, _ *wire.Conn, q LogsQuery) ([]logs.Line, error) {
		return c.Logs.Tail(q.BotID, max(1, min(q.Limit, logs.TailLines)), q.Filter), nil
	}))
	c.Mux.Handle("logs.search", wire.Func(func(_ context.Context, _ *wire.Conn, q LogsQuery) ([]logs.Line, error) {
		return c.Logs.Search(q.BotID, q.Filter, q.From, q.To, max(1, min(q.Limit, 5000)))
	}))
	c.Mux.HandleStream("logs.follow", func(ctx context.Context, _ *wire.Conn, body wire.Raw, s *wire.Sender) error {
		var q LogsQuery
		if err := wire.Unmarshal(body, &q); err != nil {
			return err
		}
		sub := c.Logs.Subscribe(q.BotID, q.Filter)
		defer c.Logs.Unsubscribe(q.BotID, sub)
		if q.Backlog > 0 {
			for _, l := range c.Logs.Tail(q.BotID, min(q.Backlog, logs.TailLines), q.Filter) {
				if err := s.Send(l); err != nil {
					return err
				}
			}
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case l := <-sub.C:
				if err := s.Send(l); err != nil {
					return err
				}
			}
		}
	})
	c.Mux.HandleStream("logs.zip", func(ctx context.Context, _ *wire.Conn, body wire.Raw, s *wire.Sender) error {
		var id int64
		if err := wire.Unmarshal(body, &id); err != nil {
			return err
		}
		return c.Logs.Zip(id, chunkWriter{s})
	})
}

var _ io.Writer = chunkWriter{}
