//go:build linux

package tgproxy

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestPeerUIDOfLoopbackConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			defer func() { _ = c.Close() }()
			_, _ = c.Read(make([]byte, 1)) // wait until the server end closes
		}
	}()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if uid, ok := peerUID(c); !ok || uid != os.Getuid() {
		t.Fatalf("peer uid %d %v, want %d", uid, ok, os.Getuid())
	}
}

// A bot's proxy port answers its bot and closes on any other uid.
func TestProxyPortServesOnlyItsBot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to connect as another uid")
	}
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("needs bash for /dev/tcp")
	}
	const botUID = 12347
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	p := &Proxy{UID: func(int64) int { return botUID }}
	if err := p.Start(1, "main", port); err != nil {
		t.Fatal(err)
	}
	defer p.Stop(1, "main")
	ask := func(uid uint32) string {
		cmd := exec.Command("/bin/bash", "-c", `exec 3<>/dev/tcp/127.0.0.1/`+strconv.Itoa(port)+`; printf 'GET / HTTP/1.0\r\n\r\n' >&3; head -c 12 <&3`) //nolint:gosec // a fixed script and a port number
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
		out, _ := cmd.Output()
		return string(out)
	}
	if got := ask(botUID); !strings.HasPrefix(got, "HTTP/1.") {
		t.Fatalf("the bot got %q", got)
	}
	if got := ask(botUID + 1); got != "" {
		t.Fatalf("another bot got %q", got)
	}
	if got := ask(0); got != "" {
		t.Fatalf("root got %q", got)
	}
}
