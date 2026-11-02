//go:build !linux

package main

import "errors"

func supervised() bool { return false }

func supervise([]string) error { return errors.New("the supervisor runs only on Linux") }
