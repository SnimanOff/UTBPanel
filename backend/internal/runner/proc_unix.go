//go:build unix

package runner

import (
	"os/exec"
	"syscall"
)

func (r *Runner) command(b Bot, bin string) *exec.Cmd {
	if r.cfg.DropPrivileges && r.cfg.ExecSelf != "" {
		a := ExecArgs{UID: UID(b.ID), Limits: b.Settings.Limits.Or(r.cfg.DefaultLimits), Binary: bin}
		cmd := exec.Command(r.cfg.ExecSelf, a.Args()...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd
	}
	cmd := exec.Command(bin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if r.cfg.DropPrivileges {
		uid := uint32(UID(b.ID))
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uid, Gid: uid, Groups: []uint32{}}
	}
	return cmd
}

// signalTerm sends SIGTERM to the bot's whole process group. The group id
// is the pid (Setpgid), which also works for a process handed over by the
// previous core.
func signalTerm(p *proc) {
	if p.pid > 0 {
		syscall.Kill(-p.pid, syscall.SIGTERM)
	}
}

func signalKill(p *proc) {
	if p.pid > 0 {
		syscall.Kill(-p.pid, syscall.SIGKILL)
	}
}
