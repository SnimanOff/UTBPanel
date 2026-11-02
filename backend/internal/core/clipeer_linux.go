//go:build linux

package core

import (
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
	"golang.org/x/sys/unix"
	"os"
	"syscall"
)

func localCLI(conn *wire.Conn) bool {
	socket, ok := conn.NetConn().(syscall.Conn)
	if !ok {
		return false
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return false
	}
	var cred *unix.Ucred
	var peerErr error
	if err := raw.Control(func(fd uintptr) { cred, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil || peerErr != nil || cred == nil {
		return false
	}
	return cred.Uid == 0 || cred.Uid == uint32(os.Geteuid())
}
