//go:build unix

package counters

import "syscall"

func diskUsage(dir string) (total, used int64) {
	var st syscall.Statfs_t
	if dir == "" || syscall.Statfs(dir, &st) != nil {
		return 0, 0
	}
	total = int64(st.Blocks) * int64(st.Bsize)
	free := int64(st.Bavail) * int64(st.Bsize)
	return total, total - free
}
