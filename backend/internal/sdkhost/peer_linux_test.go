//go:build linux

package sdkhost

import (
	"net"
	"os"
	"testing"
)

func TestRejectOtherUID(t *testing.T) {
	h := newHost(t)
	if err := os.MkdirAll(h.Dir, 0o711); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", h.Path(1, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	result := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			result <- err
			return
		}
		defer c.Close()
		if err := checkPeer(c, os.Geteuid()); err != nil {
			result <- err
			return
		}
		result <- checkPeer(c, os.Geteuid()+1)
	}()
	c, err := net.Dial("unix", h.Path(1, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := <-result; err == nil {
		t.Fatal("accepted a different uid")
	}
}
