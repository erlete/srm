package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/joblog"
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

	snap := service.FleetSnapshot{
		HostTierAvailable: true,
		Runners: []service.FusedRunner{
			{
				RunnerWithOrg: service.RunnerWithOrg{Org: "acme", Local: true, Runner: core.Runner{ID: 1, Name: "r-1", OS: "Linux", Status: "online", GroupID: 4, Labels: []core.Label{{Name: "self-hosted"}}}},
				AgentVersion:  "2.334.0", Published: "2.335.1", Behind: true,
				DriftClass: service.ClassStaleDropIn, DriftDetail: "drop-in differs", HostKnown: true,
				MemCur: 1 << 30, MemPeak: 2 << 30, MemMax: 4 << 30,
			},
			{
				RunnerWithOrg: service.RunnerWithOrg{Org: "globex", Local: false, Runner: core.Runner{ID: 2, Name: "r-2", OS: "Linux", Status: "offline"}},
				AgentVersion:  "2.335.1", Published: "2.335.1",
				DriftClass: service.ClassDropInNewer, HostKnown: true,
				MemCur: -1, MemPeak: -1, MemMax: -1,
			},
		},
		Slots: []service.FusedSlot{
			{EphemeralSlot: service.EphemeralSlot{Org: "acme", Slot: "1", Active: true, Restarts: 0, UnitOK: true, MemCur: 1 << 29, MemPeak: 1 << 30, MemMax: 4 << 30}, DriftClass: service.ClassEphemeralSlot},
			{EphemeralSlot: service.EphemeralSlot{Org: "acme", Slot: "2", Active: false, Restarts: 7, UnitOK: false, MemPeak: -1, MemMax: -1, OOMKills: 3}, DriftClass: service.ClassEphemeralStuck},
		},
		Host: service.HostHealth{Loaded: true, SliceCurrent: 12 << 30, SliceMax: 16 << 30},
	}
	step(fleetMsg{snap: snap})
	step(groupsMsg{rows: []service.GroupWithOrg{{Org: "acme", Group: core.Group{ID: 1, Name: "Default", Visibility: "all", Default: true}}}})
	step(healthMsg{
		reports: []healthReport{{Org: "acme", Runners: 2, Online: 1, RetentionDays: 90, RetentionMax: 400, AgentCurrent: "2.335.1", AgentBehind: 1, AgentOK: true}},
		host:    healthHost{CapacityMode: "auto", Tools: []toolProbe{{Name: "git", Found: true}, {Name: "docker", Found: false}}},
	})
	step(settingsMsg{snap: settingsSnapshot{
		Mode:      config.ResourceModeAuto,
		Effective: config.AutoResourceLimits(),
		SliceMax:  config.AutoSliceMemoryMax,
		Path:      "/etc/srm/config.yaml",
	}})

	// Each tab.
	for _, tb := range []tab{tabHealth, tabPersistent, tabEphemeral, tabGroups, tabDrift, tabRuns, tabSettings} {
		m.tab = tb
		if v := m.View(); v.Content == "" {
			t.Fatalf("nil view on tab %d", tb)
		}
	}

	// Information panel for a runner / slot / group (the i screen).
	openInfoOn := func(tb tab, what string) {
		m.tab = tb
		mm, _ := m.openInfo()
		m = mm.(Model)
		if v := m.View(); v.Content == "" {
			t.Fatalf("nil view with %s info open", what)
		}
		m.infoOpen = false
	}
	openInfoOn(tabPersistent, "runner")
	openInfoOn(tabEphemeral, "slot")
	openInfoOn(tabGroups, "group")

	// Multi-select chip + newer-template banner (dropin-newer row above).
	m.tab = tabPersistent
	m.runners.selectAllVisible()
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with selection + banner")
	}
	m.runners.clearSelection()

	// Operation result panel (running, then done).
	m.op = opState{open: true, running: true, title: "Upgrade agent", total: 3, done: 1, msg: "upgrading r-1"}
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with op panel running")
	}
	m.op.running = false
	m.op.items = []opItem{
		{Label: "acme/r-1", Outcome: outcomeOK, Detail: "2.334.0 -> 2.335.1"},
		{Label: "acme/r-2", Outcome: outcomeSkip, Detail: "busy"},
		{Label: "globex/r-3", Outcome: outcomeRollback, Detail: "self-test failed"},
	}
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with op panel results")
	}
	m.op = opState{}

	// Typed-confirm modal.
	m.typedOpen = true
	m.typed = newTypedConfirm(m.theme, "Upgrade ALL", "Type UPGRADE ALL to proceed.", "UPGRADE ALL", "token")
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with typed-confirm open")
	}
	m.typedOpen = false

	// Dry-run preview modal.
	m.previewOpen = true
	m.preview = newPreview(m.theme, "Fix drift (preview)", "scope: all orgs", []string{"⚠ acme r-1 would refresh"})
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with preview open")
	}
	m.previewOpen = false

	// Group create/edit form.
	m.groupForm = newGroupForm(mgr.OrgNames())
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with group form open")
	}
	m.groupForm = nil

	// Runner edit (group move) form.
	m.runnerEdit = newRunnerEditForm(service.FusedRunner{
		RunnerWithOrg: service.RunnerWithOrg{Org: "acme", Runner: core.Runner{ID: 1, Name: "r-1"}}, GroupName: "ci",
	}, []string{"ci", "build", "temporal"})
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with runner edit form open")
	}
	m.runnerEdit = nil

	// Org filter modal.
	m.orgPick = newOrgFilterModal(m.theme, mgr.OrgNames(), nil)
	m.orgPickOpen = true
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with org filter open")
	}
	m.orgPickOpen = false

	// Repo-access picker (loading, then loaded).
	m.pickerOpen = true
	m.picker = newRepoPicker(m.theme, "acme", 5, "ci")
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with picker loading")
	}
	m.picker.setData(
		[]core.Repo{{ID: 1, FullName: "acme/api"}, {ID: 2, FullName: "acme/web"}},
		[]core.Repo{{ID: 1, FullName: "acme/api"}},
	)
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with picker loaded")
	}
	m.pickerOpen = false

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
	m.form = newCreateForm(mgr.OrgNames(), m.orgDefaults)
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with wizard open")
	}

	// Create wizard (ephemeral) - a distinct form, distinct title.
	m.form = newEphemeralForm(mgr.OrgNames(), m.orgDefaults)
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with ephemeral wizard open")
	}
	m.formOpen = false
	m.form = nil

	// Settings edit form.
	m.tab = tabSettings
	m.setForm = newSettingsForm(mgr.ResourceSettings(), mgr.HostPolicy())
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with settings form open")
	}
	m.setForm = nil

	// Onboard + Edit-org forms (shared OrgForm; edit mode changes the title only).
	m.tab = tabSettings
	m.onboardForm = newOnboardForm("/etc/srm", true)
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with onboard form open")
	}
	m.onboardForm = newEditOrgForm(config.OrgConfig{Name: "acme", AppID: 1, InstallationID: 2}, "/etc/srm", true)
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with edit-org form open")
	}
	if !m.onboardForm.edit {
		t.Fatal("edit-org form should carry edit=true (drives the modal title)")
	}
	m.onboardForm = nil

	// Host-manifest editor form (splits pre-placed paths vs preserved inline bodies).
	m.tab = tabSettings
	m.manifestForm = newManifestForm(core.DependencyManifest{
		AptPackages:  []string{"jq"},
		SetupScripts: []string{"/opt/setup.sh", "#!/usr/bin/env bash\necho hi\n"},
	})
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with manifest form open")
	}
	m.manifestForm = nil

	// Runs tab (durable job-log history) + run Info panel.
	step(runsMsg{history: []joblog.Meta{
		{Org: "acme", Slot: "1", RunnerName: "srm-eph-acme-1-ab", RunnerID: 101, StartedUnix: 1, EndedUnix: 31, OK: true, HasLog: true},
		{Org: "acme", Slot: "2", RunnerName: "srm-eph-acme-2-cd", RunnerID: 102, StartedUnix: 2, EndedUnix: 2, OK: false, Error: "boom"},
	}})
	m.tab = tabRuns
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view on Runs tab")
	}
	mm2, _ := m.openInfo()
	m = mm2.(Model)
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with run info open")
	}
	m.infoOpen = false

	// Retention edit form.
	m.tab = tabHealth
	m.retForm = newRetentionForm("acme", 90, 400)
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with retention form open")
	}
	m.retForm = nil

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
