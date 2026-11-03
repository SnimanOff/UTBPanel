//go:build linux

package dbgate

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// takeCopy moves a file the bot's identity wrote into a file of root's:
// only a regular file with one link is taken, never what a link points to.
func takeCopy(part, dst string) error {
	in, err := os.OpenFile(part, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // the core's temporary copy
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok || st.Nlink != 1 {
		return fmt.Errorf("dbgate: the database copy is not a plain file")
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // a new backup file of the core
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}
