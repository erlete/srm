//go:build !linux

package runner

import (
	"os/exec"
	"syscall"
)

// setProcessGroup is a no-op off Linux: the rootless-DinD lane only runs on Linux
// hosts, but internal/ must stay cross-compilable (the build covers GOOS=windows too).
func setProcessGroup(*exec.Cmd) {}

// killProcessGroup falls back to signalling the single process off Linux (there is no
// portable process-group kill). Never exercised at runtime on these platforms.
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process != nil {
		_ = cmd.Process.Signal(sig)
	}
}
