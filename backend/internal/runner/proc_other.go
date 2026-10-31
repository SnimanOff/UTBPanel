//go:build !unix

package runner

import "os/exec"

// Bots run only on Linux; this lets the core build and be tested elsewhere.
func (r *Runner) command(b Bot, bin string) *exec.Cmd { return exec.Command(bin) }

func signalTerm(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}

func signalKill(cmd *exec.Cmd) { signalTerm(cmd) }
