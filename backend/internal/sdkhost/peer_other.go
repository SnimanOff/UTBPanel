//go:build !linux

package sdkhost

import (
	"fmt"
	"net"
)

func checkPeer(_ net.Conn, uid int) error {
	if uid >= 0 {
		return fmt.Errorf("sdk: peer credentials are supported only on Linux")
	}
	return nil
}
