package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

// lineOf returns the screen row (0-indexed) of the first rendered line whose text
// (ANSI stripped) contains needle, or -1.
func lineOf(content, needle string) int {
	for y, ln := range strings.Split(content, "\n") {
		if strings.Contains(stripANSI(ln), needle) {
			return y
		}
	}
	return -1
}

// A left click on a persistent-runner row moves the cursor to exactly that row. This
// is the non-circular check of fleetDataTop: the click targets the row's ACTUAL
// rendered screen line, so a wrong data-top offset would land on the wrong row.
func TestFleetRowClickSelectsRow(t *testing.T) {
	m := sizedModel(t)
	tm, _ := m.Update(fleetMsg{snap: service.FleetSnapshot{Runners: fusedRunners(6)}}) // ra..rf
	m = tm.(Model)
	m.tab = tabPersistent
	m.layout()

	y := lineOf(m.View().Content, " rd ") // runner index 3
	if y < 0 {
		t.Fatal("runner rd not found in the rendered view")
	}
	tm, _ = m.Update(tea.MouseClickMsg{X: 20, Y: y, Button: tea.MouseLeft})
	if got := tm.(Model).runners.st.cursor(); got != 3 {
		t.Fatalf("clicking rd's row set cursor=%d, want 3 (fleetDataTop mismatch)", got)
	}
}

// A click in the leading gutter column toggles the row's multi-select checkbox (in
// addition to focusing it).
func TestFleetGutterClickTogglesSelect(t *testing.T) {
	m := sizedModel(t)
	tm, _ := m.Update(fleetMsg{snap: service.FleetSnapshot{Runners: fusedRunners(4)}})
	m = tm.(Model)
	m.tab = tabPersistent
	m.layout()

	y := lineOf(m.View().Content, " rb ") // index 1
	if y < 0 {
		t.Fatal("runner rb not found")
	}
	tm, _ = m.Update(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft}) // x in the gutter
	m = tm.(Model)
	if m.runners.st.cursor() != 1 {
		t.Fatalf("gutter click should also focus the row, cursor=%d want 1", m.runners.st.cursor())
	}
	if got := len(m.runners.selectedRunners()); got != 1 {
		t.Fatalf("gutter click should toggle selection, selected=%d want 1", got)
	}
}

// The banner path is accounted for: with a newer-template row present (banner shown),
// a row click still lands on the right row (validates the banner height offset).
func TestFleetRowClickWithBanner(t *testing.T) {
	m := sizedModel(t)
	rs := fusedRunners(5)
	rs[0].DriftClass, rs[0].HostKnown = service.ClassDropInNewer, true // -> NewerOnDisk>0 -> banner
	tm, _ := m.Update(fleetMsg{snap: service.FleetSnapshot{Runners: rs, HostTierAvailable: true}})
	m = tm.(Model)
	m.tab = tabPersistent
	m.layout()
	if m.newerBanner() == "" {
		t.Fatal("expected the newer-template banner to be present for this test")
	}

	y := lineOf(m.View().Content, " rd ") // index 3
	if y < 0 {
		t.Fatal("runner rd not found")
	}
	tm, _ = m.Update(tea.MouseClickMsg{X: 20, Y: y, Button: tea.MouseLeft})
	if got := tm.(Model).runners.st.cursor(); got != 3 {
		t.Fatalf("row click with banner set cursor=%d, want 3 (banner offset mismatch)", got)
	}
}

// The Groups tab renders its table first with no banner above it (preTable=0); a row
// click must map correctly (validates that per-tab branch of fleetDataTop).
func TestGroupsRowClickSelectsRow(t *testing.T) {
	m := sizedModel(t)
	m.groups.setRows([]service.GroupWithOrg{
		{Org: "acme", Group: core.Group{ID: 1, Name: "grp-alpha", Visibility: "all"}},
		{Org: "acme", Group: core.Group{ID: 2, Name: "grp-bravo", Visibility: "all"}},
		{Org: "acme", Group: core.Group{ID: 3, Name: "grp-charlie", Visibility: "all"}},
	})
	m.tab = tabGroups
	m.layout()

	y := lineOf(m.View().Content, "grp-charlie")
	if y < 0 {
		t.Fatal("group grp-charlie not found in the rendered view")
	}
	tm, _ := m.Update(tea.MouseClickMsg{X: 20, Y: y, Button: tea.MouseLeft})
	m = tm.(Model)
	sel, ok := m.groups.selected()
	if !ok || sel.Group.Name != "grp-charlie" {
		t.Fatalf("clicking grp-charlie set selection to %q (ok=%v), want grp-charlie", sel.Group.Name, ok)
	}
}

// The Drift tab has two extra lines above its table (class strip + provenance); a row
// click must still map correctly (validates that per-tab branch of fleetDataTop).
func TestDriftRowClickSelectsRow(t *testing.T) {
	m := sizedModel(t)
	mk := func(name string) service.FusedRunner {
		return service.FusedRunner{
			RunnerWithOrg: service.RunnerWithOrg{Org: "acme", Runner: core.Runner{Name: name}},
			DriftClass:    service.ClassStuck, DriftDetail: "dead", HostKnown: true,
		}
	}
	m.snap = service.FleetSnapshot{
		HostTierAvailable: true, HostTierAt: time.Unix(1700000000, 0),
		Runners: []service.FusedRunner{mk("dz-1"), mk("dz-2"), mk("dz-3")},
	}
	m.feedFleetViews()
	m.tab = tabDrift
	m.layout()

	y := lineOf(m.View().Content, "dz-3")
	if y < 0 {
		t.Fatal("drift row dz-3 not found in the rendered view")
	}
	tm, _ := m.Update(tea.MouseClickMsg{X: 8, Y: y, Button: tea.MouseLeft})
	m = tm.(Model)
	sel, ok := m.drift.selected()
	if !ok || sel.name != "dz-3" {
		t.Fatalf("clicking dz-3's drift row selected %q (ok=%v), want dz-3", sel.name, ok)
	}
}
