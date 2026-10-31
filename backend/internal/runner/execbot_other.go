//go:build !linux

package runner

import "errors"

// ExecBot works only on Linux.
func ExecBot(args []string) error { return errors.New("exec-bot runs only on Linux") }
