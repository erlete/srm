package tui

import (
	"context"
	"testing"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/secrets"
	"github.com/erlete/srm/internal/service"
)

// The incremental "/" filter must narrow the visible rows on EVERY list view,
// not just Persistent - the four views below each match a different haystack but
// share the same filterState behavior.

func TestRunnersFilterNarrows(t *testing.T) {
	v := newRunnersView(NewTheme())
	v.setRows([]service.RunnerWithOrg{
		{Org: "acme", Runner: core.Runner{Name: "r-1", Status: "online"}},
		{Org: "globex", Runner: core.Runner{Name: "r-2", Status: "offline"}},
	})
	if len(v.rows) != 2 {
		t.Fatalf("unfiltered rows = %d, want 2", len(v.rows))
	}
	v.flt.input.SetValue("globex")
	v.applyFilter()
	if len(v.rows) != 1 || v.rows[0].Org != "globex" {
		t.Fatalf("filtered rows = %d, want 1 globex", len(v.rows))
	}
}

func TestEphemeralFilterNarrows(t *testing.T) {
	v := newEphemeralView(NewTheme())
	v.setRows([]service.EphemeralSlot{
		{Org: "acme", Slot: "1", Active: true, UnitOK: true},
		{Org: "globex", Slot: "2", Active: true, UnitOK: true},
	})
	v.flt.input.SetValue("acme")
	v.applyFilter()
	if len(v.rows) != 1 || v.rows[0].Org != "acme" {
		t.Fatalf("filtered rows = %d, want 1 acme", len(v.rows))
	}
}

func TestGroupsFilterNarrows(t *testing.T) {
	v := newGroupsView(NewTheme())
	v.setRows([]service.GroupWithOrg{
		{Org: "acme", Group: core.Group{Name: "Default", Visibility: "all"}},
		{Org: "globex", Group: core.Group{Name: "ci", Visibility: "selected"}},
	})
	v.flt.input.SetValue("selected")
	v.applyFilter()
	if len(v.rows) != 1 || v.rows[0].Group.Name != "ci" {
		t.Fatalf("filtered rows = %d, want 1 ci", len(v.rows))
	}
}

func TestHealthFilterNarrows(t *testing.T) {
	v := newHealthView(NewTheme())
	v.setReports([]healthReport{{Org: "acme"}, {Org: "globex"}})
	v.flt.input.SetValue("glob")
	v.applyFilter()
	if len(v.reports) != 1 || v.reports[0].Org != "globex" {
		t.Fatalf("filtered reports = %d, want 1 globex", len(v.reports))
	}
}

// The root model must route "/" to whichever list tab is active, and must refuse
// it on Settings. start/stop focus must round-trip on each filterable tab.
func TestFilterDispatchPerTab(t *testing.T) {
	mgr := service.New(&config.Config{Orgs: []config.OrgConfig{{Name: "acme"}}}, secrets.EnvStore{}, "")
	m := New(context.Background(), mgr)

	for _, tb := range []tab{tabPersistent, tabEphemeral, tabGroups, tabHealth} {
		m.tab = tb
		if !m.filterableTab() {
			t.Errorf("tab %d should be filterable", tb)
		}
		_ = m.startFilter()
		if !m.activeFiltering() {
			t.Errorf("tab %d: startFilter did not focus the input", tb)
		}
		m.stopFilter(true)
		if m.activeFiltering() {
			t.Errorf("tab %d: stopFilter did not blur the input", tb)
		}
	}

	m.tab = tabSettings
	if m.filterableTab() {
		t.Error("Settings tab must not be filterable")
	}
}
