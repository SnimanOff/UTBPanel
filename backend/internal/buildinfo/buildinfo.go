// Package buildinfo holds version data set by ldflags at build time.
package buildinfo

import "fmt"

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a one-line version description.
func String(name string) string {
	return fmt.Sprintf("%s %s (commit %s, built %s)", name, Version, Commit, Date)
}
