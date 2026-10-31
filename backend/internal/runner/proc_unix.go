//go:build unix

package runner

import (
	"os/exec"
	"syscall"
)

func (r *Runner) command(b Bot, bin string) *exec.Cmd {
	cmd := exec.Command(bin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if r.cfg.DropPrivileges {
		uid := uint32(UID(b.ID))
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uid, Gid: uid, Groups: []uint32{}}
	}
	return cmd
}

// signalTerm sends SIGTERM to the bot's whole process group.
func signalTerm(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}

func signalKill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
