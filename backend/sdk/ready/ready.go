// Package ready reports that migrations and warmup have completed.
// Without a core connection the signal is remembered for reconnection.
package ready

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
)

type Caller interface {
	Call(context.Context, string, any, any) error
}

type State struct{ signaled atomic.Bool }

func (s *State) Signal(ctx context.Context, c Caller) error {
	s.signaled.Store(true)
	return s.Resend(ctx, c)
}

func (s *State) Resend(ctx context.Context, c Caller) error {
	if !s.signaled.Load() || c == nil {
		return nil
	}
	err := c.Call(ctx, "sdk.ready", nil, nil)
	if errors.Is(err, wire.ErrNotConnected) || errors.Is(err, wire.ErrClosed) {
		return nil
	}
	return err
}
