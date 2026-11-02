package app

import "errors"

// ErrRestart asks the command's serve loop to reopen its restored state after
// Run has stopped all listeners and closed the old database handles.
var ErrRestart = errors.New("restart after restore")

type RestartRequest struct {
	Apply   func() error
	Done    chan error
	Replied <-chan struct{}
}
