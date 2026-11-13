package botconfig

import (
	"encoding/json"
	"fmt"
)

// Releases are the per-bot settings of versions and deploys: how many
// stored versions stay and how a fresh deploy is watched and rolled back.
// Durations are in seconds.
type Releases struct {
	Keep         int     `json:"keep" yaml:"keep"`
	Autorollback bool    `json:"autorollback" yaml:"autorollback"`
	Window       int     `json:"window" yaml:"window"`
	ReadyAfter   int     `json:"ready_after" yaml:"ready_after"`
	MaxCrashes   int     `json:"max_crashes" yaml:"max_crashes"`
	ErrShare     float64 `json:"err_share" yaml:"err_share"`
	MinUpdates   int     `json:"min_updates" yaml:"min_updates"`
}

// DefaultReleases are used until a bot sets its own.
func DefaultReleases() Releases {
	return Releases{Keep: 5, Autorollback: true, Window: 60, ReadyAfter: 15, MaxCrashes: 2, ErrShare: 0.2, MinUpdates: 10}
}

// Validate checks the ranges; zero is not a valid value of any number.
func (r Releases) Validate() error {
	switch {
	case r.Keep < 2 || r.Keep > 50:
		return fmt.Errorf("releases.keep: from 2 to 50 versions")
	case r.Window < 10 || r.Window > 3600:
		return fmt.Errorf("releases.window: from 10 to 3600 seconds")
	case r.ReadyAfter < 1 || r.ReadyAfter > 300:
		return fmt.Errorf("releases.ready_after: from 1 to 300 seconds")
	case r.ReadyAfter > r.Window:
		return fmt.Errorf("releases.ready_after cannot be longer than releases.window")
	case r.MaxCrashes < 1 || r.MaxCrashes > 20:
		return fmt.Errorf("releases.max_crashes: from 1 to 20")
	case r.ErrShare <= 0 || r.ErrShare > 1:
		return fmt.Errorf("releases.err_share: above 0 and at most 1")
	case r.MinUpdates < 1 || r.MinUpdates > 10000:
		return fmt.Errorf("releases.min_updates: from 1 to 10000")
	}
	return nil
}

// ParseReleases reads the stored section; empty means the defaults and
// missing fields keep their defaults.
func ParseReleases(raw json.RawMessage) (Releases, error) {
	r := DefaultReleases()
	if len(raw) == 0 || string(raw) == "null" {
		return r, nil
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return DefaultReleases(), err
	}
	return r, nil
}
