package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/secrets"
	"github.com/erlete/srm/internal/service"
)

// Bulk multi-select must select all visible rows, toggle back to empty, and keep a
// selected row selected even after a filter hides it (selectedRunners reads from
// the full set, since destroy/upgrade/recreate act on it).
func TestRunnersSelectionSemantics(t *testing.T) {
	v := newRunnersView(NewTheme())
	v.setSize(120, 20)
	v.setRows(fusedRunners(4)) // ra, rb, rc, rd

	v.selectAllVisible()
	if got := len(v.selectedRunners()); got != 4 {
		t.Fatalf("select-all = %d, want 4", got)
	}
	v.selectAllVisible() // toggle -> clear
	if got := len(v.selectedRunners()); got != 0 {
		t.Fatalf("deselect-all = %d, want 0", got)
	}

	v.st.setCursor(0) // ra
	v.toggleSelect()
	if got := len(v.selectedRunners()); got != 1 {
		t.Fatalf("toggle one = %d, want 1", got)
	}
	// Filter ra out of the visible rows; it must remain in the selection set.
	v.flt.input.SetValue("rb")
	v.applyFilter()
	if got := len(v.rows); got != 1 {
		t.Fatalf("filtered visible rows = %d, want 1", got)
	}
	if got := len(v.selectedRunners()); got != 1 {
		t.Fatalf("selection lost after filtering the row out: %d, want 1", got)
	}
}

// The org display filter (o) hides rows whose org is not in the visible set; a nil
// set shows everything.
func TestVisibleRunnersOrgFilter(t *testing.T) {
	m := sizedModel(t)
	rs := []service.FusedRunner{
		{RunnerWithOrg: service.RunnerWithOrg{Org: "acme", Runner: core.Runner{Name: "a"}}},
		{RunnerWithOrg: service.RunnerWithOrg{Org: "beta", Runner: core.Runner{Name: "b"}}},
	}
	if got := len(m.visibleRunners(rs)); got != 2 {
		t.Fatalf("nil filter = %d, want 2 (show all)", got)
	}
	m.orgVisible = map[string]bool{"acme": true}
	got := m.visibleRunners(rs)
	if len(got) != 1 || got[0].Org != "acme" {
		t.Fatalf("filtered = %d rows, want just acme", len(got))
	}
}

// The wheel scrolls the active tab's selectable on non-table tabs too: on Settings
// it steps the Lifecycle menu.
func TestWheelScrollsSettingsLifecycle(t *testing.T) {
	m := sizedModel(t)
	m.tab = tabSettings
	start := m.lifecycle.cursor
	tm, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = tm.(Model)
	if m.lifecycle.cursor != start+1 {
		t.Fatalf("wheel down on Settings: lifecycle cursor = %d, want %d", m.lifecycle.cursor, start+1)
	}
}

// tabAtX must resolve a later tab correctly, where several preceding label widths
// accumulate (guards the cumulative-width math beyond tab index 1).
func TestTabBarClickLaterTab(t *testing.T) {
	m := sizedModel(t)
	cx := 0
	for i := 0; i < 4; i++ { // width of Health..Groups (tab 0 is active)
		if tab(i) == m.tab {
			cx += lipgloss.Width(m.theme.TabOn.Render(tabNames[i]))
		} else {
			cx += lipgloss.Width(m.theme.TabOff.Render(tabNames[i]))
		}
	}
	tm, _ := m.Update(tea.MouseClickMsg{X: cx + 1, Y: m.tabBarRow(), Button: tea.MouseLeft})
	m = tm.(Model)
	if m.tab != tabDrift {
		t.Fatalf("clicking tab index 4 -> %v, want tabDrift", m.tab)
	}
}

// The recreate/destroy op progress must reach total (100%): each item emits a
// completed-count event AFTER its work. Even when the work errors (no GitHub auth
// here), the final Index must equal len(targets).
func TestDestroyOpProgressReachesTotal(t *testing.T) {
	cfg := &config.Config{Orgs: []config.OrgConfig{{Name: "acme"}}}
	mgr := service.New(cfg, secrets.EnvStore{}, "")
	targets := []service.FusedRunner{
		{RunnerWithOrg: service.RunnerWithOrg{Org: "acme", Runner: core.Runner{ID: 1, Name: "a"}}},
		{RunnerWithOrg: service.RunnerWithOrg{Org: "acme", Runner: core.Runner{ID: 2, Name: "b"}}},
	}
	ch := make(chan core.ProgressEvent, 8)
	items, _ := destroyRunnersOp(targets).Run(context.Background(), mgr, ch)
	close(ch)

	last := 0
	for ev := range ch {
		if ev.Total != len(targets) {
			t.Errorf("progress Total = %d, want %d", ev.Total, len(targets))
		}
		if ev.Index > last {
			last = ev.Index
		}
	}
	if last != len(targets) {
		t.Fatalf("last progress Index = %d, want %d (bar never reaches 100%%)", last, len(targets))
	}
	if len(items) != len(targets) {
		t.Fatalf("op produced %d result rows, want %d", len(items), len(targets))
	}
}
