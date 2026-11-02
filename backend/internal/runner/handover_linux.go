//go:build linux

package runner

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// pidfd refers to the process itself, not to its pid number: it stays
// valid when the process is reparented and cannot point to a reused pid.
func pidfd(pid int) (*os.File, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "pidfd:"+strconv.Itoa(pid)), nil
}

// waitPidFD blocks until the process exits: a pidfd becomes readable then.
func waitPidFD(f *os.File) {
	fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, -1)
		if err == unix.EINTR {
			continue
		}
		if n > 0 || err != nil {
			return
		}
	}
}
