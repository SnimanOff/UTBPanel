package api

import (
	"context"

	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
)

// Actor converts the caller into the actor sent to the core, so the core's
// journal knows who asked.
func Actor(ctx context.Context) journal.Actor {
	c := CallerFrom(ctx)
	src := string(c.Source)
	if src == "" {
		src = journal.SourceUI
	}
	return journal.Actor{Source: src, Name: c.Name, IP: c.IP}
}
