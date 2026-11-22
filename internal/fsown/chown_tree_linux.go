//go:build linux

package fsown

import (
	"io/fs"
	"syscall"
)

// statOwner returns the owner and hard-link count of fi.
func statOwner(fi fs.FileInfo) (uid, gid int, nlink uint64, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0, false
	}
	return int(st.Uid), int(st.Gid), uint64(st.Nlink), true
}
