//go:build linux

package runner

import (
	"io/fs"
	"syscall"
	"time"
)

// accessTime returns the file's last-access time. On Linux with the default
// relatime mount, atime tracks "last read (day granularity)", which is the right
// signal for cache eviction — recently-used entries survive a prune.
func accessTime(fi fs.FileInfo) time.Time {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Atim.Sec, st.Atim.Nsec)
	}
	return fi.ModTime()
}
