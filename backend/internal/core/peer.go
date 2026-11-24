package core

import (
	"net"

	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

func (c *Core) peerMux(conn net.Conn, hello wire.Hello) (*wire.Mux, error) {
	if err := authorizeCorePeer(conn, c.Cfg, hello.Proto); err != nil {
		return nil, err
	}
	switch hello.Proto {
	case wire.ProtoEdgeCore:
		return c.edgeMux, nil
	case wire.ProtoCorePanel, wire.ProtoCoreCLI:
		return c.Mux, nil
	default:
		return nil, wire.Errorf("forbidden", "unknown core peer protocol")
	}
}

func corePeerUIDAllows(cfg config.Core, localUID, peerUID uint32, protocol string) bool {
	// A peer running as root or the core's own Unix user is already trusted
	// with the core data. This also supports local all-in-one installations.
	local := peerUID == 0 || peerUID == localUID
	switch protocol {
	case wire.ProtoEdgeCore:
		return local || peerUID == uint32(cfg.EdgeUID)
	case wire.ProtoCorePanel:
		return local || peerUID == uint32(cfg.PanelUID)
	case wire.ProtoCoreCLI:
		return local
	default:
		return false
	}
}
