package botconfig

import (
	"context"
	"sync"
)

// Guard serializes configuration operations and is reentrant through ctx.
// It is separate from SQLite write locks: a Telegram request must not keep
// a database transaction open while waiting for the network.
type Guard struct {
	once  sync.Once
	token chan struct{}
}

func (g *Guard) Lock(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, func() {}, err
	}
	if ctx.Value(g) == true {
		return ctx, func() {}, nil
	}
	g.once.Do(func() { g.token = make(chan struct{}, 1) })
	select {
	case g.token <- struct{}{}:
		return context.WithValue(ctx, g, true), func() { <-g.token }, nil
	case <-ctx.Done():
		return ctx, func() {}, ctx.Err()
	}
}
