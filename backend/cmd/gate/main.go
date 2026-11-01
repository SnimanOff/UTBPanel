// Command gate is the access key of the panel: a local proxy that
// forwards the browser to the panel over mTLS with a key kept in the TPM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/SnimanOff/UTBPanel/backend/internal/buildinfo"
	"github.com/SnimanOff/UTBPanel/backend/internal/gate"
	"github.com/SnimanOff/UTBPanel/backend/internal/gatefile"
)

func main() {
	version := flag.Bool("version", false, "print version")
	noTPM := flag.Bool("no-tpm", false, "keep the device key under DPAPI instead of the TPM")
	listen := flag.String("listen", gate.DefaultListen, "local address")
	flag.Parse()
	if *version {
		fmt.Println(buildinfo.String("gate"))
		return
	}
	if err := run(*listen, *noTPM); err != nil {
		fail(err)
	}
}

func run(listen string, noTPM bool) error {
	block, err := gatefile.ReadSelf()
	if err != nil {
		return err
	}
	base, err := os.UserConfigDir() // %APPDATA% on Windows
	if err != nil {
		return err
	}
	stateDir := filepath.Join(base, "tgpanel")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(stateDir, "gate.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	log := slog.New(slog.NewTextHandler(logFile, nil))
	g, err := gate.New(gate.Config{
		Block: block, StateDir: stateDir, Listen: listen, NoTPM: noTPM, Log: log,
		OpenBrowser: openBrowser,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		errc <- g.Run(ctx)
		stop()
	}()
	// The tray lives on the main thread until "Выход" or the end of Run.
	tray(ctx, stop, g)
	err = <-errc
	if errors.Is(err, gate.ErrPortBusy) {
		// Most likely this gate is already running: just open the panel.
		return fmt.Errorf("порт %s занят: gate уже запущен или порт занят другой программой.\n\n%v", listen, err)
	}
	return err
}
