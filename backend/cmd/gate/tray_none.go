//go:build !windows && !linux && !(darwin && cgo)

package main

import (
	"context"

	"github.com/SnimanOff/UTBPanel/backend/internal/gate"
)

// tray: no tray here (macOS needs a cgo build for it); gate runs until
// stopped.
func tray(ctx context.Context, _ context.CancelFunc, _ *gate.Gate) { <-ctx.Done() }
