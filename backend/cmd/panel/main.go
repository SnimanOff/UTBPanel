package main

import (
	"context"
	"flag"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/panel"
)

func main() {
	app.Main("panel", []app.Command{
		{Name: "serve", Usage: "run the panel", Run: serve},
		{Name: "keys issue", Usage: "--owner|--child --name <name> > gate-<name>.exe", Run: keysIssue},
		{Name: "keys revoke", Usage: "<name|serial>", Run: keysRevoke},
		{Name: "keys list", Usage: "list access keys", Run: keysList},
	})
}

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cf := app.AddConfigFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.DefaultPanel()
	if done, err := cf.Load(&cfg); done || err != nil {
		return err
	}
	log := app.Logger(cfg.Common, "panel")
	p, err := panel.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	return p.Run(ctx)
}
