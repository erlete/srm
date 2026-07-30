package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/secrets"
	"github.com/erlete/srm/internal/service"
)

func sizedModel(t *testing.T) Model {
	t.Helper()
	cfg := &config.Config{Orgs: []config.OrgConfig{{Name: "acme"}}}
	mgr := service.New(cfg, secrets.EnvStore{}, "")
	m := New(context.Background(), mgr)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return tm.(Model)
}

func fusedRunners(n int) []service.FusedRunner {
	rs := make([]service.FusedRunner, n)
	for i := range rs {
		rs[i] = service.FusedRunner{
			RunnerWithOrg: service.RunnerWithOrg{Org: "acme", Runner: core.Runner{
				ID: int64(i + 1), Name: "r" + string(rune('a'+i)), OS: "Linux", Status: "online",
			}},
		}
	}
	return rs
}

// The mouse wheel scrolls the active table by moving its cursor.
func TestWheelMovesActiveTableCursor(t *testing.T) {
	m := sizedModel(t)
	tm, _ := m.Update(fleetMsg{snap: service.FleetSnapshot{Runners: fusedRunners(6)}})
	m = tm.(Model)
	m.tab = tabPersistent
	if got := m.runners.st.cursor(); got != 0 {
		t.Fatalf("initial cursor = %d, want 0", got)
	}

	tm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = tm.(Model)
	if got := m.runners.st.cursor(); got != wheelStep {
		t.Fatalf("after wheel down, cursor = %d, want %d", got, wheelStep)
	}

	tm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = tm.(Model)
	if got := m.runners.st.cursor(); got != 0 {
		t.Fatalf("after wheel up, cursor = %d, want 0", got)
	}
}

// A left click on the tab bar switches to the clicked tab.
func TestTabBarClickSwitchesTab(t *testing.T) {
	m := sizedModel(t)
	if m.tab != tabHealth {
		t.Fatalf("default tab = %v, want tabHealth", m.tab)
	}

	// Column range of the second tab (Persistent): the nav bar spans the full width
	// via tabWidths, so start-of-tab-1 = the first segment's full width.
	startTab1 := m.tabWidths()[0]
	x := startTab1 + 1
	y := m.tabBarRow()

	tm, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m = tm.(Model)
	if m.tab != tabPersistent {
		t.Fatalf("after clicking tab 1 at (%d,%d), tab = %v, want tabPersistent", x, y, m.tab)
	}

	// A click off the tab-bar row does nothing.
	tm, _ = m.Update(tea.MouseClickMsg{X: x, Y: y + 5, Button: tea.MouseLeft})
	m = tm.(Model)
	if m.tab != tabPersistent {
		t.Fatalf("click off the tab bar changed the tab to %v", m.tab)
	}
}
