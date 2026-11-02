//go:build !unix

package runner

import "os/exec"

// Bots run only on Linux; this lets the core build and be tested elsewhere.
func (r *Runner) command(b Bot, bin string) *exec.Cmd { return exec.Command(bin) }

func signalTerm(p *proc) {
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
}

func signalKill(p *proc) { signalTerm(p) }
