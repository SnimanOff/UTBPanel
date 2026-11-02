package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"

	"github.com/SnimanOff/UTBPanel/backend/internal/pki"
)

// The first owner device has no one to approve it in the panel: it is
// approved here, over SSH, after comparing the code with the gate window.

func devicesList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("devices list", flag.ContinueOnError)
	ca, db, _, err := openCA(ctx, fs, args)
	if err != nil {
		return err
	}
	defer db.Close()
	ds, err := ca.PendingDevices(ctx)
	if err != nil {
		return err
	}
	if len(ds) == 0 {
		fmt.Println("no pending devices")
		return nil
	}
	for _, d := range ds {
		fmt.Printf("%d\t%s\t%s\t%s\t%s\n", d.ID, d.Name, d.Kind, d.IP, d.Code)
	}
	return nil
}

func devicesApprove(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("devices approve", flag.ContinueOnError)
	ca, db, cfg, err := openCA(ctx, fs, args)
	if err != nil {
		return err
	}
	defer db.Close()
	if fs.NArg() != 1 {
		return errors.New("usage: panel devices approve <id>")
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return errors.New("usage: panel devices approve <id>")
	}
	d, err := ca.Approve(ctx, id)
	switch {
	case errors.Is(err, pki.ErrUnknown):
		return fmt.Errorf("no request %d", id)
	case errors.Is(err, pki.ErrNotPending), errors.Is(err, pki.ErrUsed):
		return fmt.Errorf("request %d is %s", id, d.State)
	case err != nil:
		return err
	}
	fmt.Printf("approved %s (%s), code %s\n", d.Name, d.Kind, d.Code)
	notifyPanel(ctx, cfg)
	return nil
}
