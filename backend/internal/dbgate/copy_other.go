//go:build !linux

package dbgate

import "os"

// takeCopy moves the copy in place; bots have no identity of their own here.
func takeCopy(part, dst string) error { return os.Rename(part, dst) }
