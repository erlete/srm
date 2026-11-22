//go:build !linux && !windows

package diskfree

import (
	"errors"
	"io/fs"
)

func free(string) (avail, total uint64, err error) {
	return 0, 0, errors.New("free space is not supported on this OS")
}

func deviceOf(string) (uint64, bool)     { return 0, false }
func statDev(fs.FileInfo) (uint64, bool) { return 0, false }
func usage(fi fs.FileInfo) uint64        { return uint64(fi.Size()) }
