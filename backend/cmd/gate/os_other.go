//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/SnimanOff/UTBPanel/backend/internal/gate"
)

func openBrowser(url string) {
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	exec.Command(cmd, url).Start()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gate:", err)
	os.Exit(1)
}

// tray: no tray outside Windows; gate runs until stopped.
func tray(ctx context.Context, _ context.CancelFunc, _ *gate.Gate) { <-ctx.Done() }
