//go:build linux

package backup

import (
	"golang.org/x/sys/unix"
	"os"
)

func openFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}
