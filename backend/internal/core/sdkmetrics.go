package core

import (
	"context"

	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/sdkhost"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

type MetricSelection struct {
	BotID int64         `cbor:"1,keyasint"`
	Names []string      `cbor:"2,keyasint"`
	Actor journal.Actor `cbor:"9,keyasint"`
}

func (c *Core) registerSDKMetrics() {
	c.SDKMetrics = &sdkhost.Metrics{DB: c.DB, Count: c.Count, LimitEvent: func(id, dropped int64) {
		c.Ev.Emit("sdk_metric_limit", id, events.Warn, map[string]any{"dropped": dropped, "limit": 200})
	}}
	c.Mux.Handle("sdk.metrics.list", wire.Func(func(ctx context.Context, _ *wire.Conn, id int64) ([]sdkhost.Metric, error) {
		if err := c.botExists(ctx, id); err != nil {
			return nil, err
		}
		return c.SDKMetrics.List(ctx, id)
	}))
	c.Mux.Handle("sdk.metrics.select", wire.Func(func(ctx context.Context, _ *wire.Conn, in MetricSelection) (any, error) {
		if err := c.botExists(ctx, in.BotID); err != nil {
			return nil, err
		}
		return nil, c.Journal.Do(ctx, in.Actor, "sdk.metrics.select", in.BotID, in.Names, func() error { return c.SDKMetrics.Select(ctx, in.BotID, in.Names) })
	}))
}
