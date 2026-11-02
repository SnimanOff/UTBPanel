package main

import (
	"context"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
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
		{Name: "upgrade", Usage: "< core: replace the core without stopping the bots", Run: upgrade},
		{Name: "backup restore", Usage: "< archive.backup (clean installation only)", Run: backupRestore},
	})
}
