package core

import (
	"context"
	"strconv"
	"sync"

	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/panics"
)

type panicParsers struct {
	mu sync.Mutex
	m  map[string]*panics.Parser
}

// panicParser returns the parser of one bot instance.
func (c *Core) panicParser(botID int64, instance string) *panics.Parser {
	key := strconv.FormatInt(botID, 10) + "/" + instance
	c.parsers.mu.Lock()
	defer c.parsers.mu.Unlock()
	if c.parsers.m == nil {
		c.parsers.m = map[string]*panics.Parser{}
	}
	p := c.parsers.m[key]
	if p == nil {
		version := func() string { return c.instanceVersion(botID, instance) }
		p = &panics.Parser{
			Module: func() string {
				r, err := c.Releases.Get(botID, version())
				if err != nil {
					return ""
				}
				return r.Build.Module
			},
			Found: func(pn panics.Panic) {
				pn.Version = version()
				data := map[string]any{"kind": pn.Kind, "frame": pn.Frame, "signature": pn.Signature, "stack": pn.Stack, "version": pn.Version}
				if instance != "" {
					data["instance"] = instance
				}
				c.Ev.Emit("panic", botID, events.Error, data)
			},
		}
		c.parsers.m[key] = p
	}
	return p
}

func (c *Core) instanceVersion(botID int64, instance string) string {
	if instance == "canary" {
		if b, _, ok := c.Runner.Canary(botID); ok {
			return b.Version
		}
	}
	cur, _, _ := c.versions(context.Background(), botID)
	return cur
}
