package main

import (
	"context"
	"flag"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/edge"
)

func main() {
	app.Main("edge", []app.Command{
		{Name: "serve", Usage: "run the edge", Run: serve},
	})
}

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cf := app.AddConfigFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.DefaultEdge()
	if done, err := cf.Load(&cfg); done || err != nil {
		return err
	}
	log := app.Logger(cfg.Common, "edge")
	p, err := edge.New(cfg, log)
	if err != nil {
		return err
	}
	return p.Run(ctx)
}
