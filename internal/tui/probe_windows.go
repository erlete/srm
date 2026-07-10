package tui

import "golang.org/x/sys/windows"

// elevated reports whether srm runs with an elevated (Administrator) token - the
// privilege every host-mutating srm operation needs on Windows. Mirrors
// cli.isElevated; any error reads as not-elevated (fail safe).
func elevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}
