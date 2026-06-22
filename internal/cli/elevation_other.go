//go:build !windows

package cli

import "os"

// isElevated reports whether srm is running with the privilege its mutating commands
// need. On Linux that is root (uid 0): srm manages systemd units, /etc/srm, /var/lib/srm,
// and the agent install tree, all root-owned. The Windows analog (an elevated token)
// lives in elevation_windows.go, so root.go and the first-run flow can gate on one
// OS-agnostic predicate.
func isElevated() bool { return os.Geteuid() == 0 }
