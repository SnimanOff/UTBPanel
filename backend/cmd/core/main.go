package main

import (
	"context"
	"flag"
	"log/slog"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
)

func main() {
	app.Main("core", []app.Command{
		{Name: "serve", Usage: "run the core", Run: serve},
		{Name: "exec-bot", Usage: "(internal) set limits and uid, then become the bot", Run: func(_ context.Context, args []string) error {
			return runner.ExecBot(args)
		}},
		{Name: "masterkey export", Usage: "> master.key", Run: masterkeyExport},
		{Name: "masterkey import", Usage: "< master.key (only while there are no bots)", Run: masterkeyImport},
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
	slog.SetDefault(p.Log) // redacted
	return p.Run(ctx)
}
