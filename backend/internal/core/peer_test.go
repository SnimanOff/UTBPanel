package core

import (
	"testing"

	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

func TestCorePeerUIDProtocolPolicy(t *testing.T) {
	cfg := config.DefaultCore()
	const coreUID uint32 = 50
	for _, tc := range []struct {
		name     string
		uid      uint32
		protocol string
		want     bool
	}{
		{"edge", uint32(cfg.EdgeUID), wire.ProtoEdgeCore, true},
		{"edge cannot manage the core", uint32(cfg.EdgeUID), wire.ProtoCorePanel, false},
		{"edge cannot act as the CLI", uint32(cfg.EdgeUID), wire.ProtoCoreCLI, false},
		{"panel", uint32(cfg.PanelUID), wire.ProtoCorePanel, true},
		{"panel cannot act as edge", uint32(cfg.PanelUID), wire.ProtoEdgeCore, false},
		{"panel cannot act as the CLI", uint32(cfg.PanelUID), wire.ProtoCoreCLI, false},
		{"unrecognized Unix peer", 51, wire.ProtoEdgeCore, false},
		{"local CLI", coreUID, wire.ProtoCoreCLI, true},
		{"root CLI", 0, wire.ProtoCoreCLI, true},
		{"local edge", coreUID, wire.ProtoEdgeCore, true},
		{"local panel", coreUID, wire.ProtoCorePanel, true},
		{"unsupported protocol", coreUID, "unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := corePeerUIDAllows(cfg, coreUID, tc.uid, tc.protocol); got != tc.want {
				t.Fatalf("protocol %q with UID %d was allowed = %v, want %v", tc.protocol, tc.uid, got, tc.want)
			}
		})
	}
}
