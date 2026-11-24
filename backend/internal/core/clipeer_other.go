//go:build !linux

package core

import (
	"net"

	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Non-Linux local installations retain Unix socket filesystem permissions.
func authorizeCorePeer(net.Conn, config.Core, string) error { return nil }

func localCLI(*wire.Conn) bool { return true }
