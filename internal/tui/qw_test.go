package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

// The runner group-edit Select always offers Default first, dedups the org's
// groups, and includes the runner's current group even if it wasn't in the list.
func TestGroupSelectOptions(t *testing.T) {
	if n := len(groupSelectOptions(nil, "")); n != 1 {
		t.Errorf("empty groups + no current = %d options, want 1 (Default)", n)
	}
	// Default, ci, build (dup ci dropped), temporal (current appended) = 4.
	if n := len(groupSelectOptions([]string{"ci", "build", "ci"}, "temporal")); n != 4 {
		t.Errorf("deduped + current = %d options, want 4", n)
	}
	// current already in the list -> not re-added: Default, ci = 2.
	if n := len(groupSelectOptions([]string{"ci"}, "ci")); n != 2 {
		t.Errorf("current already listed = %d options, want 2", n)
	}
}

// The Drift tab's incremental filter narrows the visible rows by class/org/name.
func TestDriftFilter(t *testing.T) {
	v := newDriftView(NewTheme())
	v.setSize(120, 20)
	v.setRows(service.FleetSnapshot{Runners: []service.FusedRunner{
		{RunnerWithOrg: service.RunnerWithOrg{Org: "acme", Runner: core.Runner{Name: "ra"}}, HostKnown: true, DriftClass: service.ClassStaleDropIn, DriftDetail: "x"},
		{RunnerWithOrg: service.RunnerWithOrg{Org: "beta", Runner: core.Runner{Name: "rb"}}, HostKnown: true, DriftClass: service.ClassStuck, DriftDetail: "y"},
	}})
	if len(v.rows) != 2 {
		t.Fatalf("unfiltered drift rows = %d, want 2", len(v.rows))
	}
	v.flt.input.SetValue("acme")
	v.applyFilter()
	if len(v.rows) != 1 || v.rows[0].org != "acme" {
		t.Fatalf("filtered drift rows = %d, want 1 (acme)", len(v.rows))
	}
}

// i opens the Information panel on the Health tab (org card) and the Drift tab
// (resolving the row back to its runner) - not just the runner/slot/group tabs.
func TestInfoOpensOnHealthAndDrift(t *testing.T) {
	// Health org card.
	mh := sizedModel(t)
	mh.health.setReports([]healthReport{{Org: "acme", Runners: 2, Online: 2}}, healthHost{CapacityMode: "auto"})
	mh.tab = tabHealth
	tm, _ := mh.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	mh = tm.(Model)
	if !mh.infoOpen {
		t.Fatal("i on Health did not open the Information panel")
	}
	if v := mh.View(); !strings.Contains(v.Content, "Information") || !strings.Contains(v.Content, "acme") {
		t.Error("Health info panel missing the Information label or the org")
	}

	// Drift row -> resolves to the runner's info.
	md := sizedModel(t)
	tm, _ = md.Update(fleetMsg{snap: service.FleetSnapshot{
		HostTierAvailable: true,
		Runners: []service.FusedRunner{{
			RunnerWithOrg: service.RunnerWithOrg{Org: "acme", Runner: core.Runner{ID: 1, Name: "ra"}},
			HostKnown:     true, DriftClass: service.ClassStuck, DriftDetail: "unit not active",
		}},
	}})
	md = tm.(Model)
	md.tab = tabDrift
	tm, _ = md.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	md = tm.(Model)
	if !md.infoOpen {
		t.Fatal("i on Drift did not open the Information panel")
	}
	if md.infoRunner == nil || md.infoRunner.Runner.Name != "ra" {
		t.Error("Drift info did not resolve back to the runner")
	}
}

// space / A on a tab that does not support multi-select gives an explanatory
// status instead of silently doing nothing.
func TestSelectFeedbackOnUnsupportedTab(t *testing.T) {
	m := sizedModel(t)
	m.tab = tabGroups
	tm, _ := m.Update(tea.KeyPressMsg{Code: ' ', Text: " "}) // space = Select
	m = tm.(Model)
	if !strings.Contains(m.status, "multi-select") {
		t.Errorf("space on Groups gave no feedback, status = %q", m.status)
	}
}
