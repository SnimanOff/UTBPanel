package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Op is one write operation run inside a transaction.
type Op func(*sql.Tx) error

// ErrClosed is returned for writes after Close.
var ErrClosed = errors.New("store: writer closed")

type request struct {
	key  string
	op   Op
	done chan error // nil for batched writes
}

// Writer is the single write goroutine of a database.
//
// Important records (audit, bans, payments, settings, keys) go through Now
// and are written at once. Frequent ones (last_seen, broadcast progress,
// metrics) go through Later and are written every interval in one
// transaction. Order is kept: Now first writes everything queued before it.
type Writer struct {
	db       *DB
	interval time.Duration
	log      *slog.Logger

	in     chan request
	stop   chan struct{}
	done   chan struct{}
	closed atomic.Bool
	once   sync.Once

	queued    atomic.Int64
	lastFlush atomic.Int64 // nanoseconds
	flushes   atomic.Int64
}

// WriterStats are the writer's own metrics.
type WriterStats struct {
	Queued    int64
	LastFlush time.Duration
	Flushes   int64
}

// NewWriter starts the write goroutine; interval 0 means 10 s.
func NewWriter(db *DB, interval time.Duration, log *slog.Logger) *Writer {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	w := &Writer{
		db: db, interval: interval, log: log,
		in:   make(chan request, 1024),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go w.loop()
	return w
}

// Now writes op immediately, together with anything queued before it.
func (w *Writer) Now(ctx context.Context, op Op) error {
	if w.closed.Load() {
		return ErrClosed
	}
	r := request{op: op, done: make(chan error, 1)}
	select {
	case w.in <- r:
	case <-ctx.Done():
		return ctx.Err()
	case <-w.stop:
		return ErrClosed
	}
	select {
	case err := <-r.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		select {
		case err := <-r.done:
			return err
		default:
			return ErrClosed
		}
	}
}

// Later queues op for the next batch. A non-empty key replaces an earlier
// queued op with the same key, so repeated updates of one row cost one write.
func (w *Writer) Later(key string, op Op) error {
	if w.closed.Load() {
		return ErrClosed
	}
	select {
	case w.in <- request{key: key, op: op}:
		return nil
	case <-w.stop:
		return ErrClosed
	}
}

// Flush writes the queued batch now and waits for it.
func (w *Writer) Flush(ctx context.Context) error {
	return w.Now(ctx, func(*sql.Tx) error { return nil })
}

// Stats returns the writer's metrics.
func (w *Writer) Stats() WriterStats {
	return WriterStats{
		Queued:    w.queued.Load(),
		LastFlush: time.Duration(w.lastFlush.Load()),
		Flushes:   w.flushes.Load(),
	}
}

// Close writes the remaining queue and stops the goroutine.
func (w *Writer) Close() error {
	w.once.Do(func() {
		w.closed.Store(true)
		close(w.stop)
	})
	<-w.done
	return nil
}

type batch struct {
	ops   []request
	byKey map[string]int
}

func (b *batch) add(r request) {
	if r.key != "" {
		if i, ok := b.byKey[r.key]; ok {
			b.ops[i] = r
			return
		}
		b.byKey[r.key] = len(b.ops)
	}
	b.ops = append(b.ops, r)
}

func (w *Writer) loop() {
	defer close(w.done)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	b := &batch{byKey: map[string]int{}}
	for {
		select {
		case r := <-w.in:
			if r.done == nil {
				b.add(r)
				w.queued.Store(int64(len(b.ops)))
				continue
			}
			r.done <- w.flush(b, &r)
		case <-t.C:
			if len(b.ops) > 0 {
				w.flush(b, nil)
			}
		case <-w.stop:
			// Take what is already in the channel, then write everything.
		drain:
			for {
				select {
				case r := <-w.in:
					if r.done == nil {
						b.add(r)
					} else {
						r.done <- w.flush(b, &r)
					}
				default:
					break drain
				}
			}
			if len(b.ops) > 0 {
				w.flush(b, nil)
			}
			return
		}
	}
}

// flush writes the batch and then now (if any) in one transaction. Each
// batched op runs under a savepoint: a failing op is logged and skipped
// without losing the rest. The error of now is returned to its caller.
func (w *Writer) flush(b *batch, now *request) error {
	start := time.Now()
	var nowErr error
	err := w.db.Tx(context.Background(), func(tx *sql.Tx) error {
		for i, r := range b.ops {
			if err := savepoint(tx, r.op); err != nil {
				w.log.Error("batched write failed", "n", i, "key", r.key, "err", err)
			}
		}
		if now != nil {
			nowErr = savepoint(tx, now.op)
		}
		return nil
	})
	b.ops = b.ops[:0]
	clear(b.byKey)
	w.queued.Store(0)
	w.lastFlush.Store(int64(time.Since(start)))
	w.flushes.Add(1)
	if err != nil {
		w.log.Error("write transaction failed", "err", err)
		return err
	}
	return nowErr
}

func savepoint(tx *sql.Tx, op Op) (err error) {
	if _, err := tx.Exec(`SAVEPOINT op`); err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic in write: %v", p)
		}
		if err != nil {
			tx.Exec(`ROLLBACK TO op`)
		}
		tx.Exec(`RELEASE op`)
	}()
	return op(tx)
}
