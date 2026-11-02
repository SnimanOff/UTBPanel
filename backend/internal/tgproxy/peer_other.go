//go:build !linux

package tgproxy

import "net"

// peerUID is known only on Linux, where bots run under their own uids.
func peerUID(net.Conn) (int, bool) { return 0, false }
