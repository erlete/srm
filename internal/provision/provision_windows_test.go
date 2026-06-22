//go:build windows

package provision

import (
	"context"
	"testing"
)

// TestDetectPkgMgrWindows checks that the package-manager detection resolves a real
// manager on this Windows host (winget on Win10 1709+/11, or choco). It is a live
// smoke test of the winget/choco selection; it skips (rather than fails) on the rare
// image with neither, so it never breaks CI on a bare runner.
func TestDetectPkgMgrWindows(t *testing.T) {
	pm, err := detectPkgMgr(context.Background())
	if err != nil {
		t.Skipf("no package manager on this host (acceptable): %v", err)
	}
	if pm.name != "winget" && pm.name != "choco" {
		t.Fatalf("detectPkgMgr returned unexpected manager %q", pm.name)
	}
	t.Logf("detected package manager: %s", pm.name)
}
