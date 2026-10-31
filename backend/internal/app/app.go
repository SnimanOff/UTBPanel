// Package app holds what the edge, core and panel commands share:
// subcommands, logging, signals and process stats.
package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/buildinfo"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
	"github.com/SnimanOff/UTBPanel/backend/internal/procstat"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Command is one subcommand such as "serve" or "bots list".
type Command struct {
	Name  string // may contain spaces: "bots start"
	Usage string
	Run   func(ctx context.Context, args []string) error
}

// Main dispatches os.Args to commands and exits with their status.
func Main(prog string, cmds []Command) {
	os.Exit(run(prog, cmds, os.Args[1:], os.Stderr))
}

func run(prog string, cmds []Command, args []string, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-version" || args[0] == "version") {
		fmt.Println(buildinfo.String(prog))
		return 0
	}
	sort.Slice(cmds, func(i, j int) bool { return len(cmds[i].Name) > len(cmds[j].Name) })
	joined := strings.Join(args, " ")
	for _, c := range cmds {
		if joined == c.Name || strings.HasPrefix(joined, c.Name+" ") {
			n := len(strings.Fields(c.Name))
			ctx, stop := SignalContext()
			err := c.Run(ctx, args[n:])
			stop()
			if err != nil {
				if err != flag.ErrHelp {
					fmt.Fprintf(stderr, "%s %s: %v\n", prog, c.Name, err)
				}
				return 1
			}
			return 0
		}
	}
	fmt.Fprintf(stderr, "usage: %s <command>\n\ncommands:\n", prog)
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Name < cmds[j].Name })
	for _, c := range cmds {
		fmt.Fprintf(stderr, "  %-24s %s\n", c.Name, c.Usage)
	}
	fmt.Fprintf(stderr, "  %-24s %s\n", "--version", "print version")
	return 2
}

// SignalContext is cancelled on SIGTERM or SIGINT.
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
}

// ConfigFlags adds --config and --print-config to fs.
type ConfigFlags struct {
	path  *string
	print *bool
}

// AddConfigFlags registers the config flags.
func AddConfigFlags(fs *flag.FlagSet) ConfigFlags {
	return ConfigFlags{
		path:  fs.String("config", "", "YAML config file (or TGPANEL_CONFIG)"),
		print: fs.Bool("print-config", false, "print the resulting config and exit"),
	}
}

// Load loads cfg and handles --print-config: it returns done=true after
// printing.
func (f ConfigFlags) Load(cfg any) (done bool, err error) {
	if err := config.Load(cfg, *f.path); err != nil {
		return false, err
	}
	if *f.print {
		fmt.Print(config.Print(cfg))
		return true, nil
	}
	return false, nil
}

// Logger builds the process logger: text for development, JSON in containers.
func Logger(c config.Common, name string) *slog.Logger {
	var lvl slog.Level
	lvl.UnmarshalText([]byte(c.LogLevel))
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if c.LogFormat == "text" {
		h = slog.NewTextHandler(os.Stderr, opts)
	} else {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	log := slog.New(h).With("proc", name)
	slog.SetDefault(log)
	return log
}

// Hello returns the handshake data of this program for proto.
func Hello(proto, prog string, version, minVersion int, caps ...string) wire.Hello {
	return wire.Hello{
		Proto: proto, Version: version, MinVersion: minVersion, Caps: caps,
		Name: prog + " " + buildinfo.Version,
	}
}

// EventProcStats carries a process' CPU and RAM to the core: the core
// cannot see processes of other containers (separate PID namespaces).
const EventProcStats = "proc.stats"

// ReportStats sends process stats over cl every interval until ctx ends.
func ReportStats(ctx context.Context, cl *wire.Client, name string, interval time.Duration) {
	var s procstat.Sampler
	s.Sample(name)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cl.Notify(EventProcStats, s.Sample(name))
		}
	}
}

// Shutdown runs fn with a deadline for a graceful stop.
func Shutdown(log *slog.Logger, timeout time.Duration, fns ...func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for _, fn := range fns {
		if err := fn(ctx); err != nil {
			log.Warn("shutdown step failed", "err", err)
		}
	}
}
