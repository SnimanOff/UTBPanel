package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/panel"
	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// openCA opens panel.db and the CA next to (or without) a running panel.
func openCA(ctx context.Context, fs *flag.FlagSet, args []string) (*pki.CA, *store.DB, config.Panel, error) {
	cf := app.AddConfigFlags(fs)
	if err := fs.Parse(args); err != nil {
		return nil, nil, config.Panel{}, err
	}
	cfg := config.DefaultPanel()
	if _, err := cf.Load(&cfg); err != nil {
		return nil, nil, cfg, err
	}
	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, "panel.db"), migrations.Panel())
	if err != nil {
		return nil, nil, cfg, err
	}
	ca, err := pki.Open(ctx, filepath.Join(cfg.DataDir, "pki"), db)
	if err != nil {
		db.Close()
		return nil, nil, cfg, err
	}
	return ca, db, cfg, nil
}

// notifyPanel tells a running panel to reload its key registry.
func notifyPanel(ctx context.Context, cfg config.Panel) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, err := wire.Dial(ctx, wire.PanelSock(cfg.RunDir), nil, app.Hello(wire.ProtoPanelCLI, "panel-cli", 1, 1), nil)
	if err != nil {
		return // the panel is not running; it will read panel.db on start
	}
	defer c.Close()
	c.Call(ctx, "keys.reload", nil, nil)
}

func keysIssue(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("keys issue", flag.ContinueOnError)
	owner := fs.Bool("owner", false, "owner key")
	child := fs.Bool("child", false, "child key")
	name := fs.String("name", "", "key name, e.g. home-pc")
	ca, db, cfg, err := openCA(ctx, fs, args)
	if err != nil {
		return err
	}
	defer db.Close()
	if *owner == *child {
		return errors.New("choose one of --owner or --child")
	}
	if cfg.PublicAddr == "" {
		return errors.New("TGPANEL_PUBLIC_ADDR is not set: gate.exe would not know the server address")
	}
	if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return errors.New("refusing to write gate.exe to a terminal; redirect it: > gate-" + *name + ".exe")
	}
	kind := pki.Child
	if *owner {
		kind = pki.Owner
	}
	b, err := ca.NewGate(ctx, kind, *name, panel.GateServers(cfg))
	if err != nil {
		return err
	}
	if err := pki.WriteGate(os.Stdout, cfg.GateExe, b); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "gate-%s.exe: %s key, enroll within 24 hours\n", *name, kind)
	notifyPanel(ctx, cfg)
	return nil
}

func keysRevoke(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("keys revoke", flag.ContinueOnError)
	ca, db, cfg, err := openCA(ctx, fs, args)
	if err != nil {
		return err
	}
	defer db.Close()
	if fs.NArg() != 1 {
		return errors.New("usage: panel keys revoke <name|serial>")
	}
	k, err := ca.RevokeKey(ctx, fs.Arg(0), true)
	if err != nil {
		return err
	}
	fmt.Printf("revoked %s (%s, %s)\n", k.Name, k.Kind, k.Serial)
	notifyPanel(ctx, cfg)
	return nil
}

func keysList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("keys list", flag.ContinueOnError)
	ca, db, _, err := openCA(ctx, fs, args)
	if err != nil {
		return err
	}
	defer db.Close()
	keys, err := ca.List(ctx)
	if err != nil {
		return err
	}
	fmt.Print(pki.FormatList(keys))
	return nil
}
