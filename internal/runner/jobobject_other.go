//go:build !windows

package runner

import (
	"io"
	"os/exec"

	"github.com/erlete/srm/internal/config"
)

// runConfinedJob (non-Windows stub) just runs the command. Job Objects are a Windows
// concept; the Linux build never executes the Windows ephemeral path (it bounds
// runners with a systemd cgroup slice instead). This exists only so the package
// cross-compiles on linux for CI.
func runConfinedJob(cmd *exec.Cmd, _ config.ResourceLimits, _ io.Writer) error {
	return cmd.Run()
}
