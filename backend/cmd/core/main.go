package main

import (
	"context"
	"errors"
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
		{Name: "bots list", Usage: "bots with states", Run: botsList},
		{Name: "bots start", Usage: "<name>", Run: botsOp("bots start")},
		{Name: "bots stop", Usage: "<name>", Run: botsOp("bots stop")},
		{Name: "bots restart", Usage: "<name>", Run: botsOp("bots restart")},
		{Name: "masterkey export", Usage: "> master.key", Run: masterkeyExport},
		{Name: "masterkey import", Usage: "< master.key (only while there are no bots)", Run: masterkeyImport},
		{Name: "backup restore", Usage: "< archive.backup (clean installation only)", Run: backupRestore},
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
	for {
		p, err := core.New(ctx, cfg, log)
		if err != nil {
			return err
		}
		slog.SetDefault(p.Log)
		err = p.Run(ctx)
		if errors.Is(err, app.ErrRestart) && ctx.Err() == nil {
			continue
		}
		return err
	}
}
