package botconfig

import (
	"bytes"
	"fmt"
	"io"

	"github.com/SnimanOff/UTBPanel/backend/internal/logs"
	"github.com/SnimanOff/UTBPanel/backend/internal/users"
	"go.yaml.in/yaml/v3"
)

// ReleaseFile is tgpanel.yaml from a bot release: the settings document
// without version. Unlike an edit it may omit sections; those stay as they
// are, so a repository can own only the settings it cares about.
type ReleaseFile struct {
	Version      *string               `yaml:"version"` // ignored: the file follows the code, not a revision
	Env          *map[string]string    `yaml:"env"`
	Rename       *map[string]string    `yaml:"rename"`
	Secrets      *[]string             `yaml:"secrets"`
	Antiflood    *users.FloodSettings  `yaml:"antiflood"`
	Closed       *Closed               `yaml:"closed"`
	Texts        *map[string]string    `yaml:"texts"`
	TZ           *string               `yaml:"tz"`
	Flags        *map[string]FlagValue `yaml:"flags"`
	Profile      *Profile              `yaml:"profile"`
	Limits       *Limits               `yaml:"limits"`
	Stop         *Stop                 `yaml:"stop"`
	GitHub       *GitHub               `yaml:"github"`
	Alerts       *[]Alert              `yaml:"alerts"`
	LogPatterns  *[]logs.Pattern       `yaml:"log_patterns"`
	CrashUpdates *bool                 `yaml:"crash_updates"`
	Updates      *string               `yaml:"updates"`
}

// ParseRelease reads tgpanel.yaml; unknown sections are refused.
func ParseRelease(raw []byte) (ReleaseFile, error) {
	var f ReleaseFile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		if err == io.EOF {
			return f, nil // an empty file changes nothing
		}
		return f, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return f, fmt.Errorf("expected one settings document")
	}
	return f, nil
}

// Over returns base with the sections present in the file replaced.
func (f ReleaseFile) Over(base Document) (Document, error) {
	d := base
	if f.Env != nil {
		d.Env = *f.Env
	}
	if f.Rename != nil {
		d.Rename = *f.Rename
	}
	if f.Secrets != nil {
		d.Secrets = *f.Secrets
	}
	if f.Antiflood != nil {
		d.Antiflood = *f.Antiflood
	}
	if f.Closed != nil {
		d.Closed = *f.Closed
	}
	if f.Texts != nil {
		d.Texts = *f.Texts
	}
	if f.TZ != nil {
		d.TZ = *f.TZ
	}
	if f.Flags != nil {
		d.Flags = *f.Flags
	}
	if f.Profile != nil {
		d.Profile = *f.Profile
	}
	if f.Limits != nil {
		d.Limits = *f.Limits
	}
	if f.Stop != nil {
		d.Stop = *f.Stop
	}
	if f.GitHub != nil {
		d.GitHub = *f.GitHub
	}
	if f.Alerts != nil {
		d.Alerts = *f.Alerts
	}
	if f.LogPatterns != nil {
		d.LogPatterns = *f.LogPatterns
	}
	if f.CrashUpdates != nil {
		d.CrashUpdates = *f.CrashUpdates
	}
	if f.Updates != nil {
		d.Updates = *f.Updates
	}
	Normalize(&d)
	return d, Validate(d)
}
