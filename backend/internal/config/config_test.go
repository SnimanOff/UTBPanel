package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSDKReadyTimeout(t *testing.T) {
	t.Setenv("TGPANEL_SDK_READY_TIMEOUT", "90s")
	cfg := DefaultCore()
	if err := Load(&cfg, ""); err != nil {
		t.Fatal(err)
	}
	if cfg.SDKReadyTimeout != 90*time.Second {
		t.Fatal(cfg.SDKReadyTimeout)
	}
	t.Setenv("TGPANEL_SDK_READY_TIMEOUT", "0s")
	if err := Load(&cfg, ""); err == nil {
		t.Fatal("accepted zero timeout")
	}
}

func TestPriority(t *testing.T) {
	path := writeFile(t, "log_level: warn\npublic_addr: file.example\nlimits:\n  memory: 512M\n")
	t.Setenv("TGPANEL_PUBLIC_ADDR", "env.example")

	cfg := DefaultCore()
	if err := Load(&cfg, path); err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("file should override default: log_level = %q", cfg.LogLevel)
	}
	if cfg.PublicAddr != "env.example" {
		t.Errorf("env should override file: public_addr = %q", cfg.PublicAddr)
	}
	if cfg.Limits.Memory != 512<<20 {
		t.Errorf("limits.memory = %v", cfg.Limits.Memory)
	}
	if cfg.Limits.OpenFiles != 4096 {
		t.Errorf("default kept: open_files = %d", cfg.Limits.OpenFiles)
	}
}

func TestEnvTypes(t *testing.T) {
	t.Setenv("TGPANEL_TELEGRAM_IPS", "10.0.0.0/8, 192.168.0.0/16")
	t.Setenv("TGPANEL_EDGE_MAX_BODY", "2M")
	t.Setenv("TGPANEL_DEV", "true")
	cfg := DefaultEdge()
	if err := Load(&cfg, ""); err != nil {
		t.Fatal(err)
	}
	if len(cfg.TelegramIPs) != 2 || cfg.TelegramIPs[1] != "192.168.0.0/16" {
		t.Errorf("telegram_ips = %v", cfg.TelegramIPs)
	}
	if cfg.MaxBody != 2<<20 || !cfg.Dev {
		t.Errorf("max_body = %v, dev = %v", cfg.MaxBody, cfg.Dev)
	}
}

func TestValidation(t *testing.T) {
	cases := []struct {
		env, want string
	}{
		{"TGPANEL_LOG_LEVEL=loud", "log_level"},
		{"TGPANEL_TELEGRAM_IPS=not-a-net", "telegram_ips"},
		{"TGPANEL_EDGE_LISTEN_TLS=8443", "listen_tls"},
		{"TGPANEL_EDGE_MAX_BODY=lots", "TGPANEL_EDGE_MAX_BODY"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			k, v, _ := strings.Cut(c.env, "=")
			t.Setenv(k, v)
			cfg := DefaultEdge()
			err := Load(&cfg, "")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error about %s, got %v", c.want, err)
			}
		})
	}
}

func TestCoreValidation(t *testing.T) {
	t.Setenv("TGPANEL_WEBHOOK_PORT", "9000")
	cfg := DefaultCore()
	if err := Load(&cfg, ""); err == nil || !strings.Contains(err.Error(), "webhook_port") {
		t.Fatalf("got %v", err)
	}
}

func TestBadFile(t *testing.T) {
	cfg := DefaultPanel()
	if err := Load(&cfg, writeFile(t, "listen: [")); err == nil {
		t.Fatal("want yaml error")
	}
	if err := Load(&cfg, "/no/such/file"); err == nil {
		t.Fatal("want missing file error")
	}
}

func TestByteSize(t *testing.T) {
	for _, raw := range []string{"9223372036854775807G", "-9223372036854775808G"} {
		var size ByteSize
		if err := size.Set(raw); err == nil {
			t.Errorf("overflow accepted: %s", raw)
		}
	}
	for in, want := range map[string]ByteSize{"512": 512, "4K": 4096, "256M": 256 << 20, "1G": 1 << 30, "2gb": 2 << 30} {
		var b ByteSize
		if err := b.Set(in); err != nil || b != want {
			t.Errorf("%s: got %v, %v", in, b, err)
		}
	}
	if s := ByteSize(256 << 20).String(); s != "256M" {
		t.Errorf("String = %s", s)
	}
	if out := Print(DefaultCore()); !strings.Contains(out, "memory: 256M") {
		t.Errorf("print: %s", out)
	}
}
