package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/secrets"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
)

func loadCfg(fs *flag.FlagSet, args []string) (config.Core, error) {
	cf := app.AddConfigFlags(fs)
	if err := fs.Parse(args); err != nil {
		return config.Core{}, err
	}
	cfg := config.DefaultCore()
	_, err := cf.Load(&cfg)
	return cfg, err
}

// The master key never passes through the panel: export and import exist
// only here, over SSH (plan 12.2).
func masterkeyExport(ctx context.Context, args []string) error {
	cfg, err := loadCfg(flag.NewFlagSet("masterkey export", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	key, err := secrets.LoadMasterKey(cfg.KeysDir)
	if err != nil {
		return err
	}
	if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return errors.New("refusing to print the master key to a terminal; redirect it: > master.key")
	}
	_, err = os.Stdout.Write(secrets.EncodeMasterKey(key))
	return err
}

func masterkeyImport(ctx context.Context, args []string) error {
	cfg, err := loadCfg(flag.NewFlagSet("masterkey import", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
	if err != nil {
		return err
	}
	key, err := secrets.ParseMasterKey(b)
	if err != nil {
		return err
	}
	// A running core takes the key over its socket and starts using it.
	if c, err := core.DialCLI(ctx, cfg); err == nil {
		defer c.Close()
		if err := c.Call(ctx, "masterkey.import", key, nil); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "master key imported")
		return nil
	}
	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, "core.db"), migrations.Core())
	if err != nil {
		return err
	}
	defer db.Close()
	if has, err := core.HasBots(ctx, db.R); err != nil {
		return err
	} else if has {
		return secrets.ErrHasBots
	}
	if err := secrets.WriteMasterKey(cfg.KeysDir, key); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "master key imported")
	return nil
}
