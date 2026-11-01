//go:build linux

package dbgate

import (
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// SQLite is pure Go and does filesystem work on the calling OS thread.
// Give that thread only the bot's filesystem identity, including Close and
// rollback. Other core goroutines retain their own credentials.
func withIdentity(uid int, fn func() error) error {
	if uid < 0 || uid == os.Geteuid() {
		return fn()
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("dbgate: cannot switch filesystem uid")
	}
	runtime.LockOSThread()
	groups, err := unix.Getgroups()
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	if err := unix.Setgroups(nil); err != nil {
		runtime.UnlockOSThread()
		return err
	}
	oldGID, err := unix.SetfsgidRetGid(uid)
	if err != nil {
		unix.Setgroups(groups)
		runtime.UnlockOSThread()
		return err
	}
	oldUID, err := unix.SetfsuidRetUid(uid)
	if err != nil {
		unix.Setfsgid(oldGID)
		unix.Setgroups(groups)
		runtime.UnlockOSThread()
		return err
	}
	defer func() {
		// Never release a thread to the runtime with altered credentials.
		if err := unix.Setfsuid(oldUID); err != nil {
			panic(err)
		}
		if err := unix.Setfsgid(oldGID); err != nil {
			panic(err)
		}
		if err := unix.Setgroups(groups); err != nil {
			panic(err)
		}
		actual, err := unix.SetfsuidRetUid(-1)
		if err != nil || actual != oldUID {
			panic("dbgate: filesystem uid restoration failed")
		}
		runtime.UnlockOSThread()
	}()
	actual, err := unix.SetfsuidRetUid(-1)
	if err != nil || actual != uid {
		return fmt.Errorf("dbgate: filesystem uid switch failed")
	}
	actualGID, err := unix.SetfsgidRetGid(-1)
	if err != nil || actualGID != uid {
		return fmt.Errorf("dbgate: filesystem gid switch failed")
	}
	return fn()
}
