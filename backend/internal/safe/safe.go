// Package safe runs goroutines that cannot crash the process: a panic in
// one goroutine is logged with its stack and only that goroutine ends.
package safe

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// Go runs fn in a goroutine with recover.
func Go(log *slog.Logger, name string, fn func()) {
	go func() {
		defer Recover(log, name)
		fn()
	}()
}

// Recover logs a panic; use it as `defer safe.Recover(log, "what")`.
func Recover(log *slog.Logger, name string) {
	if p := recover(); p != nil {
		logPanic(log, name, p)
	}
}

// Call runs fn and turns a panic into an error.
func Call(log *slog.Logger, name string, fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			logPanic(log, name, p)
			err = fmt.Errorf("panic in %s: %v", name, p)
		}
	}()
	return fn()
}

func logPanic(log *slog.Logger, name string, p any) {
	if log == nil {
		log = slog.Default()
	}
	log.Error("panic recovered", "in", name, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
}
