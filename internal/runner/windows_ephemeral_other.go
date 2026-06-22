//go:build !windows

package runner

import "fmt"

// installSupervisorService is unsupported off Windows; the stub exists only so the
// mirror cross-compiles on the Linux CI. EnsureEphemeralSlot can never reach it on a
// non-Windows host.
func (w *windows) installSupervisorService(org, slot, svc string) error {
	return fmt.Errorf("ephemeral supervisor service is only supported on Windows")
}
