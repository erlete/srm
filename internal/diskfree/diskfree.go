// Package diskfree measures free space on a filesystem and the disk used by a tree.
package diskfree

import (
	"io/fs"
	"path/filepath"
	"time"
)

// Free returns the bytes available to unprivileged users and the total size of
// the filesystem holding path.
func Free(path string) (avail, total uint64, err error) { return free(path) }

// TreeSize returns the disk space used by root and everything below it, like
// `du -sx`: it stays on root's filesystem (so the mounts of a running container
// are not counted) and counts allocated blocks where the OS reports them. It
// stops at deadline and reports partial=true with what it counted so far.
// Entries that vanish or cannot be read are skipped.
func TreeSize(root string, deadline time.Time) (bytes uint64, partial bool) {
	rootDev, haveDev := deviceOf(root)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			partial = true
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if d.IsDir() && p != root && haveDev {
			if dev, ok := statDev(info); ok && dev != rootDev {
				return filepath.SkipDir // another filesystem mounted inside the tree
			}
		}
		bytes += usage(info)
		return nil
	})
	return bytes, partial
}
