package cli

import "golang.org/x/sys/windows"

// isElevated reports whether srm is running with an elevated (Administrator) token.
// srm manages Windows Services, ACL-restricted machine-wide directories, and the
// agent install tree, all of which require elevation - so this gates the same
// "run as Administrator" guidance the Linux build gives for root. It queries the
// current process token's elevation flag; any error is treated as not-elevated
// (fail safe: prefer the per-user path and a clear hint over a privileged action
// that would fail late).
func isElevated() bool {
	tok := windows.GetCurrentProcessToken() // pseudo-handle; no Close needed
	return tok.IsElevated()
}
