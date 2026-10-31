//go:build linux

package runner

import (
	"fmt"
	"os"
	"syscall"
)

// ExecBot is `core exec-bot`: the core starts itself with the limits in its
// arguments, this sets them on its own process and then replaces itself
// with the bot. SysProcAttr has no rlimits, and prlimit after the start
// would leave a moment when the bot runs without them.
func ExecBot(args []string) error {
	a, err := ParseExecArgs(args)
	if err != nil {
		return err
	}
	set := func(res int, v uint64) error {
		return syscall.Setrlimit(res, &syscall.Rlimit{Cur: v, Max: v})
	}
	const rlimitNPROC = 6
	steps := []struct {
		name string
		fn   func() error
	}{
		{"RLIMIT_FSIZE", func() error { return set(syscall.RLIMIT_FSIZE, uint64(a.Limits.File)) }},
		{"RLIMIT_NOFILE", func() error { return set(syscall.RLIMIT_NOFILE, uint64(a.Limits.OpenFiles)) }},
		{"RLIMIT_NPROC", func() error { return set(rlimitNPROC, uint64(a.Limits.Threads)) }},
		// No core dumps: the dump would hold the bot token in plain text.
		{"RLIMIT_CORE", func() error { return set(syscall.RLIMIT_CORE, 0) }},
		// When memory runs out, the kernel kills a bot, not the panel.
		{"oom_score_adj", func() error { return os.WriteFile("/proc/self/oom_score_adj", []byte("1000"), 0) }},
		// The panel and the core get CPU first.
		{"nice", func() error { return syscall.Setpriority(syscall.PRIO_PROCESS, 0, 10) }},
		{"setgroups", func() error { return syscall.Setgroups(nil) }},
		{"setgid", func() error { return syscall.Setgid(a.UID) }},
		{"setuid", func() error { return syscall.Setuid(a.UID) }},
	}
	for _, s := range steps {
		if s.fn == nil {
			continue
		}
		if err := s.fn(); err != nil {
			return fmt.Errorf("exec-bot: %s: %w", s.name, err)
		}
	}
	// Go ignores SIGXFSZ, so going over RLIMIT_FSIZE returns "file too
	// large" to the bot instead of killing it.
	return syscall.Exec(a.Binary, []string{a.Binary}, os.Environ())
}
