//go:build !linux

package runner

import (
	"io/fs"
	"time"
)

// accessTime falls back to mtime off-Linux (atime needs the platform stat). srm
// only runs prune on the Ubuntu host; this keeps the package building elsewhere.
func accessTime(fi fs.FileInfo) time.Time { return fi.ModTime() }
