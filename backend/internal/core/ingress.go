package core

import (
	"context"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/edge"
	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// targets caches the port and webhook secret of every bot.
type targets struct {
	mu sync.RWMutex
	m  map[int64]ingress.Target
}

func (c *Core) target(ctx context.Context, id int64) (ingress.Target, error) {
	c.targets.mu.RLock()
	t, ok := c.targets.m[id]
	c.targets.mu.RUnlock()
	if !ok {
		b, err := c.Bot(ctx, id)
		if err != nil {
			return t, err
		}
		sec, err := c.Sec.Decrypt(ctx, id, secrets.WebhookSecretName)
		if err != nil {
			return t, err
		}
		t = ingress.Target{Port: b.Port, Secret: string(sec)}
		c.targets.mu.Lock()
		if c.targets.m == nil {
			c.targets.m = map[int64]ingress.Target{}
		}
		c.targets.m[id] = t
		c.targets.mu.Unlock()
	}
	t.Running = c.Runner.IsRunning(id)
	return t, nil
}

// forgetTarget drops a cached bot (deleted, token or port changed).
func (c *Core) forgetTarget(id int64) {
	c.targets.mu.Lock()
	delete(c.targets.m, id)
	c.targets.mu.Unlock()
}

func (c *Core) newPipeline() *ingress.Pipeline {
	p := ingress.New(c.Log)
	p.Target = c.target
	return p
}

func (c *Core) registerIngress() {
	c.Mux.Handle(edge.MethodUpdate, wire.Func(func(ctx context.Context, _ *wire.Conn, u edge.Update) (edge.Reply, error) {
		return c.Pipe.Handle(ctx, u.BotID, u.Body, time.Unix(0, u.Received)), nil
	}))
}
