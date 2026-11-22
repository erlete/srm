//go:build windows

package diskfree

import (
	"io/fs"

	"golang.org/x/sys/windows"
)

func free(path string) (avail, total uint64, err error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var freeToCaller, totalBytes, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &totalBytes, &totalFree); err != nil {
		return 0, 0, err
	}
	return freeToCaller, totalBytes, nil
}

func deviceOf(string) (uint64, bool)     { return 0, false }
func statDev(fs.FileInfo) (uint64, bool) { return 0, false }
func usage(fi fs.FileInfo) uint64        { return uint64(fi.Size()) }
