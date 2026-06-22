//go:build !windows

package runner

import "fmt"

// writeServiceEnvironment is Windows-only: it sets a per-service registry Environment
// value, the analog of the Linux systemd drop-in's Environment= lines. The Linux build
// injects runner env via the drop-in instead and never calls this; the stub exists only
// so this package cross-compiles on the linux CI.
func writeServiceEnvironment(svcName string, env []string) error {
	return fmt.Errorf("per-service environment injection is only supported on Windows")
}
