//go:build linux

package tgproxy

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// peerUID finds the owner of the client end of a loopback TCP connection
// in /proc/net/tcp: TCP has no SO_PEERCRED, but the kernel lists every
// socket of the network namespace with its uid. The bots share the core's
// namespace, so this tells which bot connected.
func peerUID(c net.Conn) (int, bool) {
	local, ok1 := c.RemoteAddr().(*net.TCPAddr) // the client's end
	remote, ok2 := c.LocalAddr().(*net.TCPAddr)
	if !ok1 || !ok2 {
		return 0, false
	}
	want, other := procAddr(local), procAddr(remote)
	if want == "" || other == "" {
		return 0, false
	}
	f, err := os.Open("/proc/net/tcp")
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 || fields[1] != want || fields[2] != other {
			continue
		}
		uid, err := strconv.Atoi(fields[7])
		return uid, err == nil
	}
	return 0, false
}

// procAddr formats an IPv4 address the way /proc/net/tcp prints it: the
// 32-bit address in host (little-endian) order, then the port.
func procAddr(a *net.TCPAddr) string {
	ip := a.IP.To4()
	if ip == nil {
		return ""
	}
	return fmt.Sprintf("%02X%02X%02X%02X:%04X", ip[3], ip[2], ip[1], ip[0], a.Port)
}
