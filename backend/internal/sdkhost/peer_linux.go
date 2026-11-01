//go:build linux

package sdkhost

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func checkPeer(c net.Conn, uid int) error {
	if uid < 0 {
		return fmt.Errorf("sdk: a peer uid is required on Linux")
	}
	u, ok := c.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("sdk: not a Unix connection")
	}
	raw, err := u.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) { cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return err
	}
	if credErr != nil {
		return credErr
	}
	if cred.Uid != uint32(uid) {
		return fmt.Errorf("sdk: peer uid %d, expected %d", cred.Uid, uid)
	}
	return nil
}
