//go:build linux

package runner

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the rootless daemon in its own process group so the whole
// rootlesskit -> dockerd -> containerd tree can be signalled as a unit. Linux-only;
// the !linux stub is a no-op (this lane only runs on Linux hosts).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends sig to the ENTIRE group led by cmd's process (negative pid),
// so a SIGTERM/SIGKILL reaps the daemon's reparented children, not just the setpriv
// wrapper. Without this a leaked dockerd could survive into the next cycle and a stale
// socket could be falsely adopted.
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}
