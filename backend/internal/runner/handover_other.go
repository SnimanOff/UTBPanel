//go:build !linux

package runner

import "os"

func pidfd(int) (*os.File, error) { return nil, ErrNoHandover }

func waitPidFD(*os.File) {}
