//go:build linux

package diskfree

import (
	"io/fs"
	"os"
	"syscall"
)

func free(path string) (avail, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return st.Bavail * bs, st.Blocks * bs, nil
}

func deviceOf(path string) (uint64, bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, false
	}
	return statDev(fi)
}

func statDev(fi fs.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}

// usage is the allocated size (st_blocks is in 512-byte units), so sparse files
// count what they really take.
func usage(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Blocks) * 512
	}
	return uint64(fi.Size())
}
