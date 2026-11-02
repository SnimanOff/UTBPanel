//go:build !linux

package core

import "github.com/SnimanOff/UTBPanel/backend/internal/wire"

func localCLI(*wire.Conn) bool { return true }
