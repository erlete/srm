//go:build windows

package runner

// NewForHost returns the host orchestrator for THIS OS (Windows: the SCM-based windows
// orchestrator). It is the OS-agnostic entry point the service layer calls; the Linux
// build's orchestrator_dispatch_linux.go returns the systemd-based ubuntu one.
func NewForHost(installRoot string, opts Options) Orchestrator {
	return NewWindows(installRoot, opts)
}
