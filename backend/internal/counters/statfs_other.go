//go:build !unix

package counters

// The core runs on Linux; elsewhere disk usage is unknown.
func diskUsage(string) (int64, int64) { return 0, 0 }
