//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
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
