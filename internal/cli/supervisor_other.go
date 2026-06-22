//go:build !windows

package cli

import (
	"fmt"

	"github.com/erlete/srm/internal/service"
)

// runSupervisor is unsupported off Windows: the ephemeral supervisor is a Windows
// service driven by the SCM. This stub exists only so the mirror cross-compiles on
// the Linux CI; the command can never be reached on a non-Windows host.
func runSupervisor(_ *service.Manager, _, _ string) error {
	return fmt.Errorf("_runner-supervisor is only supported on Windows")
}
