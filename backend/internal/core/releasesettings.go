package core

import (
	"context"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
)

// releaseSettings reads the bot's releases section; defaults on any error.
func (c *Core) releaseSettings(ctx context.Context, botID int64) botconfig.Releases {
	st, err := c.Settings(ctx, botID)
	if err != nil {
		return botconfig.DefaultReleases()
	}
	r, err := botconfig.ParseReleases(st["releases"])
	if err != nil || r.Validate() != nil {
		return botconfig.DefaultReleases()
	}
	return r
}

// deployRules are the observation rules of a bot and whether a bad
// verdict rolls the deploy back. Rules set in the core (tests) win.
func (c *Core) deployRules(ctx context.Context, botID int64) (deploy.Rules, bool) {
	r := c.releaseSettings(ctx, botID)
	if c.rules.Window != 0 {
		return c.rules, r.Autorollback
	}
	return deploy.Rules{
		Window:     time.Duration(r.Window) * time.Second,
		ReadyAfter: time.Duration(r.ReadyAfter) * time.Second,
		MaxCrashes: r.MaxCrashes,
		ErrShare:   r.ErrShare,
		MinUpdates: r.MinUpdates,
	}, r.Autorollback
}

// pruneReleases removes stored versions beyond the bot's keep setting.
func (c *Core) pruneReleases(ctx context.Context, botID int64) {
	cur, prev, _ := c.versions(ctx, botID)
	c.Releases.PruneKeep(botID, c.releaseSettings(ctx, botID).Keep, cur, prev)
}
