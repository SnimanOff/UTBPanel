//go:build !unix && !windows

package backup

import "fmt"

func freeSpace(string) (uint64, error) {
	return 0, fmt.Errorf("disk space is unavailable on this platform")
}
