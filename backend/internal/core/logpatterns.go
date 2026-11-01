package core

import (
	"context"
	"encoding/json"

	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/logs"
)

func (c *Core) loadPatterns(ctx context.Context, botID int64) {
	var ps []logs.Pattern
	if st, err := c.Settings(ctx, botID); err == nil {
		json.Unmarshal(st["log_patterns"], &ps)
	}
	c.patterns.Set(botID, ps)
}

func (c *Core) registerPatterns(ctx context.Context) {
	c.patterns = &logs.Matcher{
		Count: func(botID int64, name string) { c.Count.Add(botID, name, 1) },
		Limit: func(botID int64, limit, line string) {
			c.Ev.Emit("limit_reached", botID, events.Error, map[string]any{"limit": limit, "source": "log", "line": line})
		},
	}
	if bots, err := c.Bots(ctx); err == nil {
		for _, b := range bots {
			c.loadPatterns(ctx, b.ID)
		}
	}
	c.onSettings = append(c.onSettings, func(ctx context.Context, botID int64, key string) {
		if key == "log_patterns" {
			c.loadPatterns(ctx, botID)
		}
	})
}
