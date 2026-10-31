package core

import (
	"context"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// DialCLI connects the core CLI (run over SSH) to a running core.
func DialCLI(ctx context.Context, cfg config.Core) (*wire.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return wire.Dial(ctx, wire.CoreSock(cfg.RunDir), nil, app.Hello(wire.ProtoCoreCLI, "core-cli", 1, 1), nil)
}
