//go:build !windows

package runner

// NewForHost returns the host orchestrator for THIS OS (Linux: the systemd-based
// ubuntu orchestrator). It is the OS-agnostic entry point the service layer calls; the
// Windows build's orchestrator_dispatch_windows.go returns the SCM-based windows one.
// Both NewUbuntu and NewWindows compile on either OS (the unused one is dead code), so
// this dispatcher is the single seam that binds the right orchestrator per build.
func NewForHost(installRoot string, opts Options) Orchestrator {
	return NewUbuntu(installRoot, opts)
}
