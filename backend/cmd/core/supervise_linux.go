//go:build linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/SnimanOff/UTBPanel/backend/internal/core"
)

// supervised: the supervisor runs unless TGPANEL_NO_SUPERVISOR is set.
func supervised() bool { return os.Getenv("TGPANEL_NO_SUPERVISOR") == "" }

// supervise is the parent of the core workers. A container ends with its
// main process, so the core cannot simply exit after handing the bots to a
// new process: this small process stays, adopts the orphaned bots as a
// subreaper, forwards SIGTERM to the current worker and exits with it.
func supervise(args []string) error {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("subreaper: %w", err)
	}
	ctlR, ctlW, err := os.Pipe()
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, append([]string{"serve", "--worker"}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{ctlW}
	cmd.Env = append(os.Environ(), core.SupervisorFDEnv+"=3")
	if err := cmd.Start(); err != nil {
		return err
	}
	ctlW.Close()
	var current atomic.Int64
	current.Store(int64(cmd.Process.Pid))

	// "worker <pid>": the previous worker handed the bots over.
	announced := make(chan int, 8)
	go func() {
		sc := bufio.NewScanner(ctlR)
		for sc.Scan() {
			if pid, err := strconv.Atoi(strings.TrimPrefix(sc.Text(), "worker ")); err == nil {
				announced <- pid
			}
		}
	}()
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for s := range sigs {
			unix.Kill(int(current.Load()), s.(syscall.Signal))
		}
	}()

	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, 0, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
	drain:
		for {
			select {
			case n := <-announced:
				current.Store(int64(n))
			default:
				break drain
			}
		}
		if int64(pid) != current.Load() {
			// An orphaned bot of a previous worker, or that worker itself.
			continue
		}
		// The announcement is written before the old worker exits; wait a
		// little in case it is still in the pipe.
		select {
		case n := <-announced:
			current.Store(int64(n))
			continue
		case <-time.After(2 * time.Second):
		}
		code := ws.ExitStatus()
		if ws.Signaled() {
			code = 128 + int(ws.Signal())
		}
		os.Exit(code)
	}
}
