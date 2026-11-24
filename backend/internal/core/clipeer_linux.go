//go:build linux

package core

import (
	"errors"
	"net"
	"os"

	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
	"golang.org/x/sys/unix"
)

func unixPeerUID(conn net.Conn) (uint32, error) {
	socket, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("core peer must use a Unix connection")
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var peerErr error
	if err := raw.Control(func(fd uintptr) { cred, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return 0, err
	}
	if peerErr != nil {
		return 0, peerErr
	}
	if cred == nil {
		return 0, errors.New("core peer credentials unavailable")
	}
	return cred.Uid, nil
}

func authorizeCorePeer(conn net.Conn, cfg config.Core, protocol string) error {
	uid, err := unixPeerUID(conn)
	if err != nil {
		return err
	}
	if !corePeerUIDAllows(cfg, uint32(os.Geteuid()), uid, protocol) {
		return wire.Errorf("forbidden", "core peer UID %d cannot use protocol %q", uid, protocol)
	}
	return nil
}

func localCLI(conn *wire.Conn) bool {
	uid, err := unixPeerUID(conn.NetConn())
	return err == nil && (uid == 0 || uid == uint32(os.Geteuid()))
}
