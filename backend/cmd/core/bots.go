package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// The emergency path over SSH:
//
//	docker compose exec core core bots list|start|stop|restart <name>
//
// It talks to the running core through its socket; the commands go to the
// core journal with the source "cli".
var cliActor = journal.Actor{Source: journal.SourceCLI, Name: "ssh"}

func dialCore(ctx context.Context, name string, args []string) (*wire.Conn, *flag.FlagSet, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	cfg, err := loadCfg(fs, args)
	if err != nil {
		return nil, fs, err
	}
	c, err := core.DialCLI(ctx, cfg)
	if err != nil {
		return nil, fs, fmt.Errorf("the core is not running (%v)", err)
	}
	return c, fs, nil
}

func botsList(ctx context.Context, args []string) error {
	c, _, err := dialCore(ctx, "bots list", args)
	if err != nil {
		return err
	}
	defer c.Close()
	var bs []core.Bot
	if err := c.Call(ctx, "bots.list", struct{}{}, &bs); err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tUSERNAME\tSTATE\tVERSION\tUPTIME\tRESTARTS")
	for _, b := range bs {
		var st runner.Status
		c.Call(ctx, "bots.status", b.ID, &st)
		up := "-"
		if st.Running {
			up = time.Since(st.Started).Round(time.Second).String()
		}
		fmt.Fprintf(w, "%d\t%s\t@%s\t%s\t%s\t%s\t%d\n", b.ID, b.Name, b.Username, b.State, b.Version, up, st.Restarts)
	}
	return w.Flush()
}

func botsOp(method string) func(context.Context, []string) error {
	return func(ctx context.Context, args []string) error {
		c, fs, err := dialCore(ctx, method, args)
		if err != nil {
			return err
		}
		defer c.Close()
		if fs.NArg() != 1 {
			return errors.New("usage: core " + method + " <name|@username|id>")
		}
		id, err := findBot(ctx, c, fs.Arg(0))
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		if err := c.Call(ctx, "bots."+method[len("bots "):], core.BotRef{BotID: id, Actor: cliActor}, nil); err != nil {
			return err
		}
		fmt.Println("ok")
		return nil
	}
}

func findBot(ctx context.Context, c *wire.Conn, ref string) (int64, error) {
	var bs []core.Bot
	if err := c.Call(ctx, "bots.list", struct{}{}, &bs); err != nil {
		return 0, err
	}
	for _, b := range bs {
		if b.Name == ref || "@"+b.Username == ref || b.Username == ref || strconv.FormatInt(b.ID, 10) == ref {
			return b.ID, nil
		}
	}
	return 0, fmt.Errorf("no bot %q", ref)
}
