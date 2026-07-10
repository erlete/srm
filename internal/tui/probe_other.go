//go:build !windows

package tui

import "os"

// elevated reports whether the process runs as root (uid 0) - the privilege every
// host-mutating srm operation needs on Linux. Mirrors cli.isElevated.
func elevated() bool { return os.Geteuid() == 0 }
