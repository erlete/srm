package tui

import (
	"strings"
	"testing"

	"github.com/erlete/srm/internal/service"
)

// The manifest-drift line reflects each state: absent when no manifest is set,
// "satisfied" when clean, a count + provision hint when items are missing, and
// "unknown" when the probe errored.
func TestManifestLines(t *testing.T) {
	v := newHealthView(NewTheme())

	v.host = healthHost{ManifestSet: false}
	if got := v.manifestLines(); got != nil {
		t.Fatalf("no manifest configured should render nothing, got %v", got)
	}

	v.host = healthHost{ManifestSet: true, ManifestOK: true}
	if !strings.Contains(strings.Join(v.manifestLines(), "\n"), "satisfied") {
		t.Error("clean manifest should render satisfied")
	}

	v.host = healthHost{ManifestSet: true, ManifestOK: true, ManifestMissing: []string{"pkg:jq", "pkg:make", "pkg:git", "pkg:node", "pkg:pnpm", "pkg:curl"}}
	joined := strings.Join(v.manifestLines(), "\n")
	if !strings.Contains(joined, "6 missing (P to provision)") {
		t.Errorf("missing manifest should show the count + hint, got:\n%s", joined)
	}
	if !strings.Contains(joined, "and 2 more") {
		t.Errorf("long missing list should elide after the first few, got:\n%s", joined)
	}

	v.host = healthHost{ManifestSet: true, ManifestOK: false}
	if !strings.Contains(strings.Join(v.manifestLines(), "\n"), "unknown") {
		t.Error("a failed drift probe should render unknown")
	}
}

// The rootless-DinD block is empty unless enabled, leads with a cross-org caveat when
// perOrgUsers is off, and marks each check with a pass/fail glyph.
func TestDinDLines(t *testing.T) {
	v := newHealthView(NewTheme())

	v.host = healthHost{DinD: service.DinDReport{Enabled: false}}
	if got := v.dindLines(); got != nil {
		t.Fatalf("disabled DinD should render nothing, got %v", got)
	}

	v.host = healthHost{DinD: service.DinDReport{
		Enabled:      true,
		CrossOrgRisk: true,
		Checks: []service.DinDCheck{
			{Name: "rootlesskit", OK: true, Detail: "/usr/bin/rootlesskit"},
			{Name: "/dev/fuse", OK: false, Detail: "absent (fuse-overlayfs storage driver unavailable)"},
		},
	}}
	joined := strings.Join(v.dindLines(), "\n")
	if !strings.Contains(joined, "rootless docker") {
		t.Error("enabled DinD should render its header")
	}
	if !strings.Contains(joined, "no cross-org boundary") {
		t.Error("cross-org risk should surface the caveat")
	}
	if !strings.Contains(joined, "✓ rootlesskit") {
		t.Error("a passing check should get the pass glyph")
	}
	if !strings.Contains(joined, "✗ /dev/fuse") || !strings.Contains(joined, "absent") {
		t.Error("a failing check should show the fail glyph + remediation detail")
	}
}
