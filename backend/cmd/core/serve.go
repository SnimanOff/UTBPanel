package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
)

// serve runs the supervisor, which runs the core as its worker (Linux), or
// the core itself.
func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cf := app.AddConfigFlags(fs)
	worker := fs.Bool("worker", false, "(internal) run the core under the supervisor")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.DefaultCore()
	if done, err := cf.Load(&cfg); done || err != nil {
		return err
	}
	if !*worker && supervised() {
		return supervise(args)
	}
	var control *os.File
	if fd, err := strconv.Atoi(os.Getenv(core.SupervisorFDEnv)); err == nil && fd > 2 {
		control = os.NewFile(uintptr(fd), "supervisor")
	}
	log := app.Logger(cfg.Common, "core")
	for {
		p, err := core.New(ctx, cfg, log)
		if err != nil {
			return err
		}
		p.Control = control
		slog.SetDefault(p.Log)
		opened, err := p.Adopt(ctx)
		if err != nil {
			return err
		}
		opened()
		err = p.Run(ctx)
		if errors.Is(err, app.ErrRestart) && ctx.Err() == nil {
			continue
		}
		return err
	}
}
