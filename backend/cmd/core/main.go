package main

import (
	"context"
	"flag"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
)

func main() {
	app.Main("core", []app.Command{
		{Name: "serve", Usage: "run the core", Run: serve},
	})
}

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cf := app.AddConfigFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.DefaultCore()
	if done, err := cf.Load(&cfg); done || err != nil {
		return err
	}
	log := app.Logger(cfg.Common, "core")
	p, err := core.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	return p.Run(ctx)
}
