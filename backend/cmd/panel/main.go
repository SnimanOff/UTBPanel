package main

import (
	"context"
	"errors"
	"flag"
	"os"

	"github.com/SnimanOff/UTBPanel/backend/internal/api"
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
		{Name: "devices list", Usage: "list devices waiting for approval", Run: devicesList},
		{Name: "devices approve", Usage: "<id>: approve after comparing the code", Run: devicesApprove},
	})
}

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cf := app.AddConfigFlags(fs)
	dev := fs.Bool("dev", false, "also serve the API without mTLS on 127.0.0.1:8080 (needs TGPANEL_DEV=1)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.DefaultPanel()
	if done, err := cf.Load(&cfg); done || err != nil {
		return err
	}
	if *dev {
		if !cfg.Dev {
			return errors.New("--dev needs TGPANEL_DEV=1")
		}
		if os.Getenv(api.InImageEnv) != "" {
			return api.ErrDevInImage
		}
	}
	cfg.Dev = *dev
	log := app.Logger(cfg.Common, "panel")
	for {
		p, err := panel.New(ctx, cfg, log)
		if err != nil {
			return err
		}
		err = p.Run(ctx)
		if errors.Is(err, app.ErrRestart) && ctx.Err() == nil {
			continue
		}
		return err
	}
}
