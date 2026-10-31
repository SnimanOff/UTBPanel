package runner

import (
	"flag"
	"fmt"
	"strconv"
)

// Limits are the resource limits of one bot.
type Limits struct {
	Memory     int64 `json:"memory,omitempty"`      // RSS bytes; enforced by the watcher
	File       int64 `json:"file,omitempty"`        // RLIMIT_FSIZE
	FilesTotal int64 `json:"files_total,omitempty"` // bot.db + DATA_DIR; only warned
	OpenFiles  int64 `json:"open_files,omitempty"`  // RLIMIT_NOFILE
	Threads    int64 `json:"threads,omitempty"`     // RLIMIT_NPROC (per uid = per bot)
}

// Or fills unset limits from defaults.
func (l Limits) Or(d Limits) Limits {
	if l.Memory <= 0 {
		l.Memory = d.Memory
	}
	if l.File <= 0 {
		l.File = d.File
	}
	if l.FilesTotal <= 0 {
		l.FilesTotal = d.FilesTotal
	}
	if l.OpenFiles <= 0 {
		l.OpenFiles = d.OpenFiles
	}
	if l.Threads <= 0 {
		l.Threads = d.Threads
	}
	return l
}

// GoMemLimit is 90 % of the memory limit, so the Go runtime of the bot
// collects garbage harder before the watcher restarts it.
func (l Limits) GoMemLimit() string {
	return strconv.FormatInt(l.Memory*9/10, 10)
}

// ExecArgs are the arguments of `core exec-bot`.
type ExecArgs struct {
	UID    int
	Limits Limits
	Binary string
}

// Args renders the command line after the program name.
func (a ExecArgs) Args() []string {
	return []string{"exec-bot",
		"--uid", strconv.Itoa(a.UID),
		"--fsize", strconv.FormatInt(a.Limits.File, 10),
		"--nofile", strconv.FormatInt(a.Limits.OpenFiles, 10),
		"--nproc", strconv.FormatInt(a.Limits.Threads, 10),
		"--", a.Binary}
}

// ParseExecArgs reads the arguments of `core exec-bot`.
func ParseExecArgs(args []string) (ExecArgs, error) {
	fs := flag.NewFlagSet("exec-bot", flag.ContinueOnError)
	var a ExecArgs
	fs.IntVar(&a.UID, "uid", -1, "uid and gid of the bot")
	fs.Int64Var(&a.Limits.File, "fsize", 0, "RLIMIT_FSIZE")
	fs.Int64Var(&a.Limits.OpenFiles, "nofile", 0, "RLIMIT_NOFILE")
	fs.Int64Var(&a.Limits.Threads, "nproc", 0, "RLIMIT_NPROC")
	if err := fs.Parse(args); err != nil {
		return a, err
	}
	if fs.NArg() != 1 || a.UID < UIDBase {
		return a, fmt.Errorf("exec-bot: usage: exec-bot --uid N [limits] -- <binary>")
	}
	a.Binary = fs.Arg(0)
	return a, nil
}
