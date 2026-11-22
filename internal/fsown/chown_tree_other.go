//go:build !linux

package fsown

import "io/fs"

// statOwner reports no owner off Linux, where srm never manages runner trees.
func statOwner(fs.FileInfo) (uid, gid int, nlink uint64, ok bool) { return 0, 0, 0, false }
