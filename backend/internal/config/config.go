// Package config loads settings for edge, core and panel.
//
// Sources, highest priority first: environment (prefix TGPANEL_), an
// optional YAML file (TGPANEL_CONFIG or --config), built-in defaults.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const envPrefix = "TGPANEL_"

// Common settings shared by all three processes.
type Common struct {
	LogLevel  string `yaml:"log_level" env:"LOG_LEVEL"`
	LogFormat string `yaml:"log_format" env:"LOG_FORMAT"` // text | json
	RunDir    string `yaml:"run_dir" env:"RUN_DIR"`       // shared sockets
	// PublicAddr is the domain or IP Telegram and gate use to reach the server.
	PublicAddr string `yaml:"public_addr" env:"PUBLIC_ADDR"`
	Dev        bool   `yaml:"dev" env:"DEV"`
}

// Edge is the gatekeeper: the only process exposed to the internet.
type Edge struct {
	Common      `yaml:",inline"`
	DataDir     string   `yaml:"data_dir" env:"EDGE_DATA_DIR"`
	ListenTLS   string   `yaml:"listen_tls" env:"EDGE_LISTEN_TLS"`
	ListenHTTP  string   `yaml:"listen_http" env:"EDGE_LISTEN_HTTP"`
	Domain      string   `yaml:"domain" env:"DOMAIN"` // Let's Encrypt; empty = self-signed
	TelegramIPs []string `yaml:"telegram_ips" env:"TELEGRAM_IPS"`
	MaxBody     ByteSize `yaml:"max_body" env:"EDGE_MAX_BODY"`
}

// Core runs bots and owns secrets.
type Core struct {
	Common       `yaml:",inline"`
	DataDir      string `yaml:"data_dir" env:"CORE_DATA_DIR"`
	KeysDir      string `yaml:"keys_dir" env:"KEYS_DIR"`
	HostProc     string `yaml:"host_proc" env:"HOST_PROC"`
	WebhookPort  int    `yaml:"webhook_port" env:"WEBHOOK_PORT"` // public edge port in WEBHOOK_URL
	BotPortFirst int    `yaml:"bot_port_first" env:"BOT_PORT_FIRST"`
	BotPortLast  int    `yaml:"bot_port_last" env:"BOT_PORT_LAST"`
	Limits       Limits `yaml:"limits"`
}

// Limits are default per-bot resource limits (plan 3.3).
type Limits struct {
	Memory     ByteSize `yaml:"memory" env:"LIMIT_MEMORY"`
	File       ByteSize `yaml:"file" env:"LIMIT_FILE"`
	FilesTotal ByteSize `yaml:"files_total" env:"LIMIT_FILES_TOTAL"`
	OpenFiles  int      `yaml:"open_files" env:"LIMIT_OPEN_FILES"`
	Threads    int      `yaml:"threads" env:"LIMIT_THREADS"`
}

// Panel serves the UI, API and MCP.
type Panel struct {
	Common  `yaml:",inline"`
	DataDir string `yaml:"data_dir" env:"PANEL_DATA_DIR"`
	Listen  string `yaml:"listen" env:"PANEL_LISTEN"`
	DevAddr string `yaml:"dev_addr" env:"PANEL_DEV_ADDR"`
	WebDir  string `yaml:"web_dir" env:"WEB_DIR"`
	GateExe string `yaml:"gate_exe" env:"GATE_EXE"`
}

func defaultCommon() Common {
	return Common{LogLevel: "info", LogFormat: "json", RunDir: "/run/tgpanel"}
}

// DefaultEdge returns edge defaults.
func DefaultEdge() Edge {
	return Edge{
		Common:      defaultCommon(),
		DataDir:     "/data/edge",
		ListenTLS:   ":8443",
		ListenHTTP:  ":80",
		TelegramIPs: []string{"149.154.160.0/20", "91.108.4.0/22"},
		MaxBody:     1 << 20,
	}
}

// DefaultCore returns core defaults.
func DefaultCore() Core {
	return Core{
		Common:       defaultCommon(),
		DataDir:      "/data/core",
		KeysDir:      "/keys",
		HostProc:     "/host/proc",
		WebhookPort:  8443,
		BotPortFirst: 20000,
		BotPortLast:  29999,
		Limits: Limits{
			Memory:     256 << 20,
			File:       1 << 30,
			FilesTotal: 2 << 30,
			OpenFiles:  4096,
			Threads:    1024,
		},
	}
}

// DefaultPanel returns panel defaults.
func DefaultPanel() Panel {
	return Panel{
		Common:  defaultCommon(),
		DataDir: "/data/panel",
		Listen:  ":443",
		DevAddr: "127.0.0.1:8080",
		WebDir:  "/srv/web",
		GateExe: "/srv/gate/gate.exe",
	}
}

// Load fills cfg (a pointer to Edge, Core or Panel holding defaults) from
// the YAML file at path (optional) and then from the environment.
func Load(cfg any, path string) error {
	if path == "" {
		path = os.Getenv(envPrefix + "CONFIG")
	}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("config file: %w", err)
		}
		if err := yaml.Unmarshal(b, cfg); err != nil {
			return fmt.Errorf("config file %s: %w", path, err)
		}
	}
	if err := fromEnv(reflect.ValueOf(cfg).Elem()); err != nil {
		return err
	}
	if v, ok := cfg.(interface{ Validate() error }); ok {
		return v.Validate()
	}
	return nil
}

func fromEnv(v reflect.Value) error {
	t := v.Type()
	for i := range t.NumField() {
		f, fv := t.Field(i), v.Field(i)
		if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeFor[ByteSize]() {
			if err := fromEnv(fv); err != nil {
				return err
			}
			continue
		}
		name := f.Tag.Get("env")
		if name == "" {
			continue
		}
		raw, ok := os.LookupEnv(envPrefix + name)
		if !ok {
			continue
		}
		if err := setValue(fv, raw); err != nil {
			return fmt.Errorf("%s%s: %w", envPrefix, name, err)
		}
	}
	return nil
}

func setValue(v reflect.Value, raw string) error {
	switch p := v.Addr().Interface().(type) {
	case *string:
		*p = raw
	case *bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		*p = b
	case *int:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return err
		}
		*p = n
	case *time.Duration:
		d, err := time.ParseDuration(raw)
		if err != nil {
			return err
		}
		*p = d
	case *ByteSize:
		return p.Set(raw)
	case *[]string:
		*p = nil
		for s := range strings.SplitSeq(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				*p = append(*p, s)
			}
		}
	default:
		return fmt.Errorf("unsupported type %s", v.Type())
	}
	return nil
}

func (c Common) validate() error {
	var errs []error
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("log_level: %q is not one of debug, info, warn, error", c.LogLevel))
	}
	if c.LogFormat != "text" && c.LogFormat != "json" {
		errs = append(errs, fmt.Errorf("log_format: %q is not text or json", c.LogFormat))
	}
	if c.RunDir == "" {
		errs = append(errs, errors.New("run_dir: must be set"))
	}
	return errors.Join(errs...)
}

func validAddr(name, addr string) error {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("%s: %q is not host:port", name, addr)
	}
	return nil
}

// Validate checks edge settings.
func (c Edge) Validate() error {
	errs := []error{c.Common.validate(), validAddr("listen_tls", c.ListenTLS)}
	if c.Domain != "" {
		errs = append(errs, validAddr("listen_http", c.ListenHTTP))
	}
	if c.DataDir == "" {
		errs = append(errs, errors.New("data_dir: must be set"))
	}
	if len(c.TelegramIPs) == 0 {
		errs = append(errs, errors.New("telegram_ips: at least one range is required"))
	}
	for _, s := range c.TelegramIPs {
		if _, err := netip.ParsePrefix(s); err != nil {
			errs = append(errs, fmt.Errorf("telegram_ips: %q is not a CIDR range", s))
		}
	}
	if c.MaxBody <= 0 {
		errs = append(errs, errors.New("max_body: must be positive"))
	}
	return errors.Join(errs...)
}

// TelegramPrefixes returns parsed Telegram ranges; call after Validate.
func (c Edge) TelegramPrefixes() []netip.Prefix {
	out := make([]netip.Prefix, 0, len(c.TelegramIPs))
	for _, s := range c.TelegramIPs {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// Validate checks core settings.
func (c Core) Validate() error {
	errs := []error{c.Common.validate()}
	if c.DataDir == "" || c.KeysDir == "" {
		errs = append(errs, errors.New("data_dir and keys_dir: must be set"))
	}
	if c.BotPortFirst < 1024 || c.BotPortLast > 65535 || c.BotPortFirst > c.BotPortLast {
		errs = append(errs, fmt.Errorf("bot ports: bad range %d-%d", c.BotPortFirst, c.BotPortLast))
	}
	switch c.WebhookPort {
	case 443, 80, 88, 8443:
	default:
		errs = append(errs, fmt.Errorf("webhook_port: Telegram accepts only 443, 80, 88 and 8443, got %d", c.WebhookPort))
	}
	l := c.Limits
	if l.Memory <= 0 || l.File <= 0 || l.FilesTotal <= 0 || l.OpenFiles <= 0 || l.Threads <= 0 {
		errs = append(errs, errors.New("limits: all limits must be positive"))
	}
	return errors.Join(errs...)
}

// Validate checks panel settings.
func (c Panel) Validate() error {
	errs := []error{c.Common.validate(), validAddr("listen", c.Listen)}
	if c.Dev {
		errs = append(errs, validAddr("dev_addr", c.DevAddr))
	}
	if c.DataDir == "" {
		errs = append(errs, errors.New("data_dir: must be set"))
	}
	return errors.Join(errs...)
}

// Print renders cfg as YAML. Configs hold no secrets, so nothing is hidden.
func Print(cfg any) string {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err.Error()
	}
	return string(b)
}
