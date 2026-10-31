package gate

import (
	"context"
	"errors"
)

// EnrollState returns the current registration state.
func (g *Gate) EnrollState() EnrollState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.enroll
}

// Enroll registers the device; implemented in the next step.
func (g *Gate) Enroll(ctx context.Context) error { return errors.New("gate: enrollment is not implemented yet") }
