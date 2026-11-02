package pki

import (
	"path/filepath"
	"testing"
)

func TestGateFile(t *testing.T) {
	exe := filepath.Join("srv", "gate", "gate.exe")
	for _, c := range []struct{ system, path, file string }{
		{"", exe, "gate-pc.exe"},
		{"windows", exe, "gate-pc.exe"},
		{"linux", filepath.Join("srv", "gate", "gate-linux-amd64"), "gate-pc"},
		{"macos", filepath.Join("srv", "gate", "gate-darwin-arm64"), "gate-pc"},
		{"macos-intel", filepath.Join("srv", "gate", "gate-darwin-amd64"), "gate-pc"},
	} {
		path, file, err := GateFile(exe, c.system, "pc")
		if err != nil || path != c.path || file != c.file {
			t.Errorf("%q: %s %s %v", c.system, path, file, err)
		}
	}
	if _, _, err := GateFile(exe, "../etc", "pc"); err == nil {
		t.Fatal("unknown system accepted")
	}
}
