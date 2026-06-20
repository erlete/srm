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

// TestRenderPaths exercises every view (tabs, wizard, modal) to guarantee the
// render code never panics. It does not touch the network: data is injected via
// the same messages the async commands emit.
func TestRenderPaths(t *testing.T) {
	cfg := &config.Config{
		Orgs: []config.OrgConfig{{Name: "acme"}, {Name: "globex"}},
	}
	mgr := service.New(cfg, secrets.EnvStore{}, "")

	m := New(context.Background(), mgr)
	step := func(msg tea.Msg) {
		tm, _ := m.Update(msg)
		m = tm.(Model)
		if v := m.View(); v.Content == "" {
			t.Fatalf("nil view layer for %T", msg)
		}
	}

	step(tea.WindowSizeMsg{Width: 120, Height: 40})

	rows := []service.RunnerWithOrg{
		{Org: "acme", Local: true, Runner: core.Runner{ID: 1, Name: "r-1", OS: "Linux", Status: "online", Labels: []core.Label{{Name: "self-hosted"}}}},
		{Org: "globex", Local: false, Runner: core.Runner{ID: 2, Name: "r-2", OS: "Linux", Status: "offline", Busy: false}},
	}
	step(runnersMsg{rows: rows})
	step(ephemeralMsg{rows: []service.EphemeralSlot{
		{Org: "acme", Slot: "1", Active: true, Restarts: 0, UnitOK: true, MemPeak: 1 << 30, MemMax: 4 << 30},
		{Org: "acme", Slot: "2", Active: false, Restarts: 7, UnitOK: false, MemPeak: -1, MemMax: -1},
	}})
	step(groupsMsg{rows: []service.GroupWithOrg{{Org: "acme", Group: core.Group{ID: 1, Name: "Default", Visibility: "all", Default: true}}}})
	step(healthMsg{reports: []healthReport{{Org: "acme", Runners: 2, Online: 1, RetentionDays: 90, RetentionMax: 400}}})
	step(settingsMsg{snap: settingsSnapshot{
		Mode:      config.ResourceModeAuto,
		Effective: config.AutoResourceLimits(),
		SliceMax:  config.AutoSliceMemoryMax,
		Path:      "/etc/srm/config.yaml",
	}})

	// Each tab.
	for _, tb := range []tab{tabPersistent, tabEphemeral, tabGroups, tabHealth, tabSettings} {
		m.tab = tb
		if v := m.View(); v.Content == "" {
			t.Fatalf("nil view on tab %d", tb)
		}
	}

	// Confirm modal.
	m.tab = tabPersistent
	m.modalOpen = true
	m.modal = newConfirm(m.theme, "Destroy runner?", "r-1 (acme) lives on THIS host.")
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with modal open")
	}
	m.modalOpen = false

	// Create wizard (persistent).
	m.formOpen = true
	m.form = newCreateForm(mgr.OrgNames())
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with wizard open")
	}

	// Create wizard (ephemeral) — a distinct form, distinct title.
	m.form = newEphemeralForm(mgr.OrgNames())
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with ephemeral wizard open")
	}
	m.formOpen = false
	m.form = nil

	// Settings edit form.
	m.tab = tabSettings
	m.setForm = newSettingsForm(mgr.ResourceSettings())
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with settings form open")
	}
	m.setForm = nil

	// Active filter on the persistent view.
	m.tab = tabPersistent
	_ = m.runners.startFilter()
	m.runners.flt.input.SetValue("globex")
	m.runners.applyFilter()
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with filter active")
	}
	m.runners.stopFilter(true)

	// Create-in-progress (streaming progress bar).
	m.creating = true
	m.createTotal, m.createDone, m.createMsg = 5, 2, "created temporal-2"
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view while creating")
	}
	m.creating = false

	// Full help.
	m.help.ShowAll = true
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with full help")
	}
}
