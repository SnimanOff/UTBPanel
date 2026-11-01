// Package botconfig models the portable, secret-free bot settings document.
package botconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"io"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/github"
	"github.com/SnimanOff/UTBPanel/backend/internal/logs"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/telegram"
	"github.com/SnimanOff/UTBPanel/backend/internal/users"
	"github.com/SnimanOff/UTBPanel/backend/sdk/flags"
	"go.yaml.in/yaml/v3"
)

type Bytes int64

func (b Bytes) MarshalYAML() (any, error) { return config.ByteSize(b).String(), nil }
func (b *Bytes) UnmarshalJSON(raw []byte) error {
	var n int64
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		var value config.ByteSize
		if err := value.Set(text); err != nil {
			return err
		}
		*b = Bytes(value)
		return nil
	}
	if err := json.Unmarshal(raw, &n); err != nil {
		return err
	}
	*b = Bytes(n)
	return nil
}
func (b *Bytes) UnmarshalYAML(n *yaml.Node) error {
	var value config.ByteSize
	if err := value.Set(n.Value); err != nil {
		return err
	}
	*b = Bytes(value)
	return nil
}

type FlagValue flags.Value

func (v FlagValue) MarshalJSON() ([]byte, error) { return flags.Value(v).MarshalJSON() }
func (v *FlagValue) UnmarshalJSON(raw []byte) error {
	var value flags.Value
	if err := value.UnmarshalJSON(raw); err != nil {
		return err
	}
	*v = FlagValue(value)
	return nil
}
func (v FlagValue) MarshalYAML() (any, error) {
	switch v.Kind {
	case "bool":
		return v.Bool, nil
	case "int":
		return v.Int, nil
	case "string":
		return v.Text, nil
	case "duration":
		return time.Duration(v.Int).String(), nil
	}
	return nil, fmt.Errorf("invalid flag type")
}
func (v *FlagValue) UnmarshalYAML(n *yaml.Node) error {
	var value any
	if err := n.Decode(&value); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return v.UnmarshalJSON(raw)
}

type Limits struct {
	Memory     Bytes `json:"memory" yaml:"memory"`
	File       Bytes `json:"file" yaml:"file"`
	FilesTotal Bytes `json:"files_total" yaml:"files_total"`
	OpenFiles  int64 `json:"open_files" yaml:"open_files"`
	Threads    int64 `json:"threads" yaml:"threads"`
}
type Closed struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	Text    string `json:"text" yaml:"text"`
}
type Stop struct {
	DropPending bool `json:"drop_pending" yaml:"drop_pending"`
}
type GitHub struct {
	Repo       string `json:"repo" yaml:"repo"`
	Asset      string `json:"asset" yaml:"asset"`
	AutoDeploy bool   `json:"autodeploy" yaml:"autodeploy"`
}
type Profile struct {
	Name             map[string]string                `json:"name" yaml:"name"`
	Description      map[string]string                `json:"description" yaml:"description"`
	ShortDescription map[string]string                `json:"short_description" yaml:"short_description"`
	Commands         map[string][]telegram.BotCommand `json:"commands" yaml:"commands"`
	Menu             *telegram.MenuButton             `json:"menu,omitempty" yaml:"menu,omitempty"`
}
type Alert struct {
	Metric  string  `json:"metric" yaml:"metric"`
	Op      string  `json:"op" yaml:"op"`
	Value   float64 `json:"value" yaml:"value"`
	Minutes int     `json:"minutes" yaml:"minutes"`
	Enabled bool    `json:"enabled" yaml:"enabled"`
}
type Group struct {
	ID   int64  `json:"id" yaml:"id"`
	Name string `json:"name" yaml:"name"`
}
type Document struct {
	Version      string               `json:"version" yaml:"version,omitempty"`
	Group        *Group               `json:"group,omitempty" yaml:"group,omitempty"`
	Env          map[string]string    `json:"env" yaml:"env"`
	Rename       map[string]string    `json:"rename" yaml:"rename"`
	Secrets      []string             `json:"secrets" yaml:"secrets"`
	Antiflood    users.FloodSettings  `json:"antiflood" yaml:"antiflood"`
	Closed       Closed               `json:"closed" yaml:"closed"`
	Texts        map[string]string    `json:"texts" yaml:"texts"`
	TZ           string               `json:"tz" yaml:"tz"`
	Flags        map[string]FlagValue `json:"flags" yaml:"flags"`
	Profile      Profile              `json:"profile" yaml:"profile"`
	Limits       Limits               `json:"limits" yaml:"limits"`
	Stop         Stop                 `json:"stop" yaml:"stop"`
	GitHub       GitHub               `json:"github" yaml:"github"`
	Alerts       []Alert              `json:"alerts" yaml:"alerts"`
	LogPatterns  []logs.Pattern       `json:"log_patterns" yaml:"log_patterns"`
	CrashUpdates bool                 `json:"crash_updates" yaml:"crash_updates"`
}
type Snapshot struct {
	Document Document `cbor:"1,keyasint"`
	Version  string   `cbor:"2,keyasint"`
}

var requiredFields = []string{"env", "rename", "secrets", "antiflood", "closed", "texts", "tz", "flags", "profile", "limits", "stop", "github", "alerts", "log_patterns", "crash_updates"}

// A settings document is a full replacement, so omitted sections must not
// silently clear settings (especially secret declarations).
func (d *Document) UnmarshalJSON(raw []byte) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return err
	}
	for _, key := range requiredFields {
		if _, ok := keys[key]; !ok {
			return fmt.Errorf("missing settings section %q", key)
		}
	}
	type plain Document
	var value plain
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&value); err != nil {
		return err
	}
	*d = Document(value)
	return nil
}

func Normalize(d *Document) {
	if d.Env == nil {
		d.Env = map[string]string{}
	}
	if d.Rename == nil {
		d.Rename = map[string]string{}
	}
	if d.Secrets == nil {
		d.Secrets = []string{}
	}
	sort.Strings(d.Secrets)
	if d.Texts == nil {
		d.Texts = map[string]string{}
	}
	if d.Flags == nil {
		d.Flags = map[string]FlagValue{}
	}
	if d.Alerts == nil {
		d.Alerts = []Alert{}
	}
	if d.LogPatterns == nil {
		d.LogPatterns = []logs.Pattern{}
	}
	if d.Profile.Name == nil {
		d.Profile.Name = map[string]string{}
	}
	if d.Profile.Description == nil {
		d.Profile.Description = map[string]string{}
	}
	if d.Profile.ShortDescription == nil {
		d.Profile.ShortDescription = map[string]string{}
	}
	if d.Profile.Commands == nil {
		d.Profile.Commands = map[string][]telegram.BotCommand{}
	}
	for lang, commands := range d.Profile.Commands {
		if commands == nil {
			d.Profile.Commands[lang] = []telegram.BotCommand{}
		}
	}
	if d.TZ == "" {
		d.TZ = "Europe/Moscow"
	}
}
func Fingerprint(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func Version(core string, d Document) string {
	d.Version = ""
	d.Group = nil
	return Fingerprint(struct {
		Core     string
		Settings Document
	}{core, d})
}
func YAML(d Document) (string, error) { raw, err := yaml.Marshal(d); return string(raw), err }
func Decode(raw []byte) (Document, error) {
	var d Document
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return d, err
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return d, fmt.Errorf("expected settings object")
	}
	keys := map[string]bool{}
	for i := 0; i < len(root.Content[0].Content); i += 2 {
		keys[root.Content[0].Content[i].Value] = true
	}
	for _, key := range requiredFields {
		if !keys[key] {
			return d, fmt.Errorf("missing settings section %q", key)
		}
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return d, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return d, fmt.Errorf("expected one settings document")
	}
	Normalize(&d)
	return d, Validate(d)
}

func Validate(d Document) error {
	for _, r := range d.Alerts {
		if err := (alerts.Rule{Metric: r.Metric, Op: r.Op, Value: r.Value, Minutes: r.Minutes, Enabled: r.Enabled}).Validate(); err != nil {
			return err
		}
	}
	if len(d.Flags) > flags.MaxFlags {
		return fmt.Errorf("too many flags")
	}
	for name, value := range d.Flags {
		if err := flags.Validate(flags.Definition{Name: name, Kind: value.Kind, Default: flags.Value(value)}); err != nil {
			return fmt.Errorf("flag %s: %w", name, err)
		}
	}
	for name := range d.Env {
		if !secrets.ValidName(name) || ReservedSecret(name) {
			return fmt.Errorf("invalid environment name %q", name)
		}
	}
	for from, to := range d.Rename {
		if !secrets.ValidName(from) || (to != "" && !secrets.ValidName(to)) {
			return fmt.Errorf("invalid environment rename")
		}
	}
	seen := map[string]bool{}
	for _, name := range d.Secrets {
		if !secrets.ValidName(name) || ReservedSecret(name) || seen[name] {
			return fmt.Errorf("invalid or duplicate secret name %q", name)
		}
		seen[name] = true
	}
	if _, err := time.LoadLocation(d.TZ); err != nil {
		return fmt.Errorf("unknown time zone %q", d.TZ)
	}
	if d.Antiflood.Limit < 0 || d.Antiflood.Strikes < 0 || d.Antiflood.BanHours < 0 {
		return fmt.Errorf("negative antiflood setting")
	}
	if d.Antiflood.Window != "" {
		if value, err := time.ParseDuration(d.Antiflood.Window); err != nil || value <= 0 {
			return fmt.Errorf("invalid antiflood window")
		}
	}
	if d.Limits.Memory < 0 || d.Limits.File < 0 || d.Limits.FilesTotal < 0 || d.Limits.OpenFiles < 0 || d.Limits.Threads < 0 {
		return fmt.Errorf("negative resource limit")
	}
	if d.GitHub.Repo != "" && !github.ValidRepo(d.GitHub.Repo) {
		return fmt.Errorf("GitHub repo must be owner/repo")
	}
	if _, err := filepath.Match(d.GitHub.Asset, "x"); err != nil {
		return fmt.Errorf("invalid GitHub asset mask: %w", err)
	}
	if err := logs.Validate(d.LogPatterns); err != nil {
		return err
	}
	languages := map[string]bool{}
	for key := range d.Profile.Name {
		languages[key] = true
	}
	for key := range d.Profile.Description {
		languages[key] = true
	}
	for key := range d.Profile.ShortDescription {
		languages[key] = true
	}
	for key := range d.Profile.Commands {
		languages[key] = true
	}
	for lang := range languages {
		if lang != "default" && (len(lang) != 2 || strings.Trim(lang, "abcdefghijklmnopqrstuvwxyz") != "") {
			return fmt.Errorf("invalid profile language %q", lang)
		}
	}
	for _, commands := range d.Profile.Commands {
		for _, cmd := range commands {
			if cmd.Command == "" || len(cmd.Command) > 32 || strings.Trim(cmd.Command, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
				return fmt.Errorf("invalid bot command %q", cmd.Command)
			}
		}
	}
	if m := d.Profile.Menu; m != nil {
		switch m.Type {
		case "default", "commands":
		case "web_app":
			if m.WebApp == nil {
				return fmt.Errorf("web_app URL is required")
			}
			u, err := url.Parse(m.WebApp.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				return fmt.Errorf("web_app URL must use HTTPS")
			}
		default:
			return fmt.Errorf("invalid menu button type")
		}
	}
	return nil
}
func ReservedSecret(name string) bool {
	switch name {
	case secrets.TokenName, secrets.WebhookSecretName, secrets.GitHubTokenName, secrets.GitHubHookName:
		return true
	}
	return false
}
