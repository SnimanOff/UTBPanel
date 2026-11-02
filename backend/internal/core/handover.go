package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/internal/tgproxy"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Seamless restart (plan 19.1, variant 3). The running core starts the new
// binary and passes it the bots: their output pipes, a pidfd of each and the
// Bot API proxy ports. The bots keep running and keep their proxy.
//
//  1. old: starts the new process with the files and the state in the env;
//  2. new: opens its databases, takes the processes and the proxy ports,
//     reports "adopted" on the ready pipe, then waits for the old one to go;
//  3. old: lets the bots go, tells the supervisor the new pid, shuts down
//     (its sockets are unlinked here) and exits; its end of the gone pipe
//     closes with it;
//  4. new: opens the core and SDK sockets; panel, edge and the bots' SDK
//     reconnect. Updates in this gap are handled by the edge.

// HandoverEnv carries the state to the new core process.
const HandoverEnv = "TGPANEL_HANDOVER"

// SupervisorFDEnv is the fd of the pipe to the supervisor in a worker.
const SupervisorFDEnv = "TGPANEL_SUPERVISOR_FD"

// HandoverTimeout bounds the start of the new process.
var HandoverTimeout = time.Minute

type handoverState struct {
	Bots    []runner.Handed    `json:"bots"`
	Proxies []tgproxy.Listener `json:"proxies"`
	// File numbers among the passed files (fd 3 is the first).
	Ready   int `json:"ready"`
	Gone    int `json:"gone"`
	Control int `json:"control"`
	Files   int `json:"files"`
}

// Upgrade is the request of core upgrade.
type Upgrade struct {
	Path string `cbor:"1,keyasint"`
}

func (c *Core) registerUpgrade() {
	// Only from the core CLI: core upgrade < core.
	c.Mux.Handle("core.upgrade", wire.Func(func(ctx context.Context, conn *wire.Conn, in Upgrade) (struct{}, error) {
		if conn.Proto() != wire.ProtoCoreCLI || !localCLI(conn) {
			return struct{}{}, wire.Errorf("forbidden", "upgrade is available only from the core CLI")
		}
		return struct{}{}, c.Journal.Do(ctx, journal.Actor{Source: journal.SourceCLI, Name: "ssh"}, "core.upgrade", 0, map[string]string{"path": in.Path}, func() error {
			return c.Upgrade(ctx, in.Path)
		})
	}))
}

// Upgrade hands the bots to a new core process started from path and shuts
// this one down. On error nothing changes: this core keeps the bots.
func (c *Core) Upgrade(ctx context.Context, path string) error {
	if runtime.GOOS != "linux" {
		return wire.Errorf("unsupported", "seamless restart needs Linux")
	}
	if c.Control == nil {
		return wire.Errorf("unsupported", "the core runs without the supervisor: restart the container instead")
	}
	if !c.upgradeMu.TryLock() {
		return wire.Errorf("busy", "an upgrade is already running")
	}
	defer c.upgradeMu.Unlock()
	if fi, err := os.Stat(path); err != nil || fi.Mode()&0o111 == 0 {
		return wire.Errorf("bad_binary", "%s is not an executable file", path)
	}

	bots, botFiles, err := c.Runner.Handover()
	if err != nil {
		return wire.Errorf("handover", "%v", err)
	}
	proxies, proxyFiles, err := c.Proxy.Handover()
	if err != nil {
		closePidFDs(bots, botFiles)
		return wire.Errorf("handover", "%v", err)
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		closePidFDs(bots, botFiles)
		closeAll(proxyFiles)
		return err
	}
	defer readyR.Close()
	goneR, goneW, err := os.Pipe()
	if err != nil {
		readyW.Close()
		closePidFDs(bots, botFiles)
		closeAll(proxyFiles)
		return err
	}

	// The pidfds are this process's own; the pipes are the runner's.
	var pidfds []*os.File
	for _, h := range bots {
		pidfds = append(pidfds, botFiles[h.PidFD])
	}
	st := handoverState{Ready: 0, Gone: 1, Control: 2}
	files := []*os.File{readyW, goneR, c.Control}
	base := len(files)
	for i := range bots {
		bots[i].Stdout += base
		bots[i].Stderr += base
		bots[i].PidFD += base
	}
	files = append(files, botFiles...)
	base = len(files)
	for i := range proxies {
		proxies[i].File += base
	}
	files = append(files, proxyFiles...)
	st.Bots, st.Proxies, st.Files = bots, proxies, len(files)
	state, err := json.Marshal(st)
	if err != nil {
		return err
	}

	cmd := exec.Command(path, os.Args[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.ExtraFiles = files
	cmd.Env = append(withoutEnv(os.Environ(), HandoverEnv, SupervisorFDEnv),
		HandoverEnv+"="+string(state),
		SupervisorFDEnv+"="+strconv.Itoa(3+st.Control))
	err = cmd.Start()
	// The child has its copies now.
	readyW.Close()
	goneR.Close()
	closeAll(pidfds)
	closeAll(proxyFiles)
	if err != nil {
		goneW.Close()
		return wire.Errorf("start_failed", "%v", err)
	}
	c.Log.Info("new core process started", "pid", cmd.Process.Pid, "binary", path, "bots", len(bots))

	readyR.SetReadDeadline(time.Now().Add(HandoverTimeout))
	b := make([]byte, 1)
	if _, err := io.ReadFull(readyR, b); err != nil || b[0] != 'A' {
		cmd.Process.Kill()
		go cmd.Wait()
		goneW.Close()
		if err == nil {
			err = errors.New("unexpected answer")
		}
		c.Log.Error("new core process failed, keeping the bots", "err", err)
		return wire.Errorf("start_failed", "the new core did not take the bots: %v", err)
	}

	// From here the bots are the new process's.
	c.Runner.Release()
	fmt.Fprintf(c.Control, "worker %d\n", cmd.Process.Pid)
	// goneW stays open until this process exits: then the new one goes on.
	c.goneW = goneW
	// Shut down after the answer has reached the CLI.
	time.AfterFunc(500*time.Millisecond, func() { close(c.handedOver) })
	return nil
}

// Adopt takes the bots from the previous core process, if this process was
// started by Upgrade. It returns a function to call once the previous
// process is gone, before the core opens its sockets.
func (c *Core) Adopt(ctx context.Context) (func(), error) {
	raw := os.Getenv(HandoverEnv)
	if raw == "" {
		return func() {}, nil
	}
	os.Unsetenv(HandoverEnv)
	var st handoverState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return nil, fmt.Errorf("handover state: %w", err)
	}
	files := make([]*os.File, st.Files)
	for i := range files {
		// The control pipe is opened by serve from SupervisorFDEnv; a second
		// os.File for the same fd would close it when collected.
		if i != st.Control {
			files[i] = os.NewFile(uintptr(3+i), "handover-"+strconv.Itoa(i))
		}
	}
	c.Runner.Adopt(st.Bots, files)
	if err := c.Proxy.Adopt(st.Proxies, files); err != nil {
		return nil, err
	}
	ready, gone := files[st.Ready], files[st.Gone]
	if _, err := ready.Write([]byte{'A'}); err != nil {
		return nil, err
	}
	ready.Close()
	c.Log.Info("bots taken over from the previous core", "bots", len(st.Bots))
	return func() {
		// EOF once the previous process exits.
		io.Copy(io.Discard, gone)
		gone.Close()
		for _, h := range st.Bots {
			if err := c.prepareSDK(h.Bot); err != nil {
				c.Log.Error("SDK socket of a taken over bot", "bot", h.Bot.ID, "err", err)
			}
			if h.Bot.Instance == "" {
				c.botStarted(ctx, h.Bot)
			}
		}
	}, nil
}

func closePidFDs(bots []runner.Handed, files []*os.File) {
	for _, h := range bots {
		files[h.PidFD].Close()
	}
}

func closeAll(files []*os.File) {
	for _, f := range files {
		f.Close()
	}
}

func withoutEnv(env []string, names ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		keep := true
		for _, n := range names {
			if strings.HasPrefix(kv, n+"=") {
				keep = false
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}
