// Package tui implements the Bubble Tea (v2) terminal UI: a tabbed, multi-org
// fleet cockpit and control plane over the service layer. Six tabs - Health,
// Persistent runners, Ephemeral runners, Groups, Drift, and Settings (Lifecycle
// operations live in a panel within Settings, not a tab of their own) -
// share a header (org filter), a spinner-driven status bar, an integrated help
// bubble, and a layered set of modals (yes/no confirm, typed-confirm, and an
// operation result panel) for actions. The Persistent and Ephemeral panels are
// deliberately SEPARATE: the two runner natures must never be mistaken, so each
// has its own columns, create/destroy flows, and addressing (name vs slot id).
//
// All GitHub/host work runs inside a tea.Cmd; Update never blocks. The Persistent,
// Ephemeral, and Drift tabs derive their rows from ONE fused fleet snapshot
// (service.FleetSnapshot) loaded in two tiers: a fast tier (GitHub list + manifest
// versions) that always works, and a host tier (a reconcile pass) that adds drift
// + memory + host health when run as root on the host and degrades gracefully off
// it. Exactly one modal/form/op panel is ever open (the singleton invariant).
package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	huh "charm.land/huh/v2"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

type tab int

const (
	tabHealth tab = iota
	tabPersistent
	tabEphemeral
	tabGroups
	tabDrift
	tabSettings
)

var tabNames = []string{"Health", "Persistent Runners", "Ephemeral Runners", "Groups", "Drift", "Settings"}

// defaultAutoEvery is the auto-refresh interval when the operator opts in with `a`.
const defaultAutoEvery = 15 * time.Second

// Model is the root Bubble Tea model.
type Model struct {
	ctx   context.Context
	mgr   *service.Manager
	theme Theme
	keys  keyMap
	help  help.Model
	spin  spinner.Model

	width, height int

	tab       tab
	orgFilter string // load scope - always "" now (all orgs); display is filtered by orgVisible

	// orgVisible is the session org display filter (nil = all). Set via the org
	// filter modal (o); the views hide rows whose org is not in the set.
	orgVisible  map[string]bool
	orgPickOpen bool
	orgPick     orgFilterModal

	runners   runnersView
	ephemeral ephemeralView
	groups    groupsView
	health    healthView
	settings  settingsView
	drift     driftView
	lifecycle lifecycleView

	// Fused fleet snapshot (Persistent / Ephemeral / Drift). Loaded in two tiers.
	snap        service.FleetSnapshot
	fleetLoaded bool
	loadedAt    time.Time
	hostCapable bool // host-mutating ops can run here (root/elevated)

	loading bool
	status  string
	stErr   bool

	// Full-screen scrollable Information panel (opened with i / enter). The subject
	// is held so async lookups (current job, a group's assigned repos) can rebuild it.
	infoOpen            bool
	info                infoView
	infoRunner          *service.FusedRunner
	infoJob             *core.RunnerJob
	infoJobState        string // "", looking, found, none, error
	infoGroup           *service.GroupWithOrg
	infoGroupRepos      []core.Repo
	infoGroupReposState string // "", looking, loaded

	// memHistory is a bounded per-runner/slot ring of recent live cgroup-memory
	// samples (keyed via memKey*), appended on each host-tier merge and rendered as a
	// sparkline in the Information panel. Keys for vanished runners are pruned.
	memHistory map[string][]int64

	// Auto-refresh.
	autoOn    bool
	autoEvery time.Duration

	// Cross-tab "newer template on disk" banner, dismissible per session.
	bannerDismissed bool

	modalOpen bool
	modal     confirmModal
	onConfirm func() tea.Cmd

	// previewModal is the dry-run-preview rung (host-wide ops show their plan).
	previewOpen bool
	preview     previewModal

	// typedConfirm is the strongest gate (fleet-wide / destructive ops).
	typedOpen bool
	typed     typedConfirmModal

	// pendingOp is the operation a confirm / typed-confirm gate is guarding; on
	// approval it is launched through the op-runner. pendingTyped, when set, is the
	// token a preview's Apply must escalate to a typed-confirm for (uninstall).
	pendingOp    *opSpec
	pendingTyped string
	// pendingTyped2 is a chained SECOND typed token (e.g. "PURGE"): when the first
	// typed-confirm arms and proceeds, a non-empty pendingTyped2 opens another
	// typed-confirm before the op runs. Used by the guarded uninstall purge.
	pendingTyped2 string
	op            opState

	formOpen bool
	form     *createForm

	setForm *settingsForm // Settings edit form (nil = closed)

	retForm *retentionForm // Health retention edit form (nil = closed)

	groupForm *groupForm // group create/edit form (nil = closed)

	runnerEdit *runnerEditForm // persistent-runner group edit (nil = closed)

	upgradeForm *upgradeForm // agent-upgrade options form (--to-version / --force; nil = closed)

	uninstallForm *uninstallForm // uninstall Step-1 scope + toggles form (nil = closed)

	onboardForm *onboardForm  // lifecycle onboard wizard (add an org; nil = closed)
	restoreOpen bool          // lifecycle restore backup picker is open
	restore     restorePicker

	// Repo-access picker (autocomplete multi-select) for a "selected" group.
	pickerOpen bool
	picker     repoPicker

	creating    bool
	prog        progress.Model
	createCh    chan core.ProgressEvent
	createResCh chan createResult
	createDone  int
	createTotal int
	createMsg   string
	createOrg   string
	createNoun  string // "runner" or "ephemeral slot" - labels the create summary
}

// New builds the root model.
func New(ctx context.Context, mgr *service.Manager) Model {
	t := NewTheme()
	sp := spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(t.Spin))
	h := help.New()
	return Model{
		ctx:         ctx,
		mgr:         mgr,
		theme:       t,
		keys:        newKeyMap(),
		help:        h,
		spin:        sp,
		prog:        progress.New(progress.WithWidth(48)),
		runners:     newRunnersView(t),
		ephemeral:   newEphemeralView(t),
		groups:      newGroupsView(t),
		health:      newHealthView(t),
		settings:    newSettingsView(t),
		drift:       newDriftView(t),
		lifecycle:   newLifecycleView(t),
		info:        newInfoView(t),
		hostCapable: hostCapable(),
		autoEvery:   defaultAutoEvery,
		loading:     true,
	}
}

// Init starts the spinner and loads the first view.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, m.reloadCurrent())
}

// reloadCurrent forces a full reload of the active tab (fast + host tier for the
// fleet tabs; the per-tab command otherwise). The host tier follows fleetMsg.
func (m *Model) reloadCurrent() tea.Cmd {
	m.loading = true
	if fleetTab(m.tab) {
		return loadFleetCmd(m.ctx, m.mgr, m.orgFilter)
	}
	return m.loadCurrent()
}

// enterTab is called on a tab switch: fleet tabs reuse the cached snapshot (instant
// switch, no reload), other tabs load on entry. It also closes the Information panel.
func (m *Model) enterTab() tea.Cmd {
	m.infoOpen = false
	if fleetTab(m.tab) {
		if m.fleetLoaded {
			m.layout()
			return nil
		}
		m.loading = true
		return loadFleetCmd(m.ctx, m.mgr, m.orgFilter)
	}
	m.loading = true
	return m.loadCurrent()
}

// loadCurrent returns the load command for a non-fleet tab (fleet tabs go through
// loadFleetCmd).
func (m Model) loadCurrent() tea.Cmd {
	switch m.tab {
	case tabGroups:
		return loadGroupsCmd(m.ctx, m.mgr, m.orgFilter)
	case tabHealth:
		return loadHealthCmd(m.ctx, m.mgr, m.orgFilter)
	case tabSettings:
		return loadSettingsCmd(m.mgr)
	default:
		return loadFleetCmd(m.ctx, m.mgr, m.orgFilter)
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// The create wizard captures the event loop while open.
	if m.formOpen {
		return m.updateForm(msg)
	}
	// The Settings edit form likewise captures the loop while open.
	if m.setForm != nil {
		return m.updateSettingsForm(msg)
	}
	// The retention edit form captures the loop while open.
	if m.retForm != nil {
		return m.updateRetentionForm(msg)
	}
	// The group create/edit form captures the loop while open.
	if m.groupForm != nil {
		return m.updateGroupForm(msg)
	}
	// The runner edit form captures the loop while open.
	if m.runnerEdit != nil {
		return m.updateRunnerEditForm(msg)
	}
	// The upgrade-options form captures the loop while open (before the preview).
	if m.upgradeForm != nil {
		return m.updateUpgradeForm(msg)
	}
	// The uninstall Step-1 form captures the loop while open (before the preview).
	if m.uninstallForm != nil {
		return m.updateUninstallForm(msg)
	}
	// The onboard wizard captures the loop while open.
	if m.onboardForm != nil {
		return m.updateOnboardForm(msg)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.SetWidth(msg.Width)
		m.layout()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		return m.onKey(msg)

	case tea.MouseMsg:
		return m.onMouse(msg)

	case fleetMsg:
		m.loading = false
		m.snap = msg.snap
		m.fleetLoaded = true
		m.loadedAt = time.Now()
		m.feedFleetViews()
		m.setFleetStatus()
		m.layout()
		if m.hostCapable {
			return m, loadHostTierCmd(m.ctx, m.mgr, m.orgFilter)
		}
		return m, nil

	case hostTierMsg:
		if msg.orgFilter != m.orgFilter {
			return m, nil // a since-changed scope; ignore the stale pass
		}
		if msg.err != nil {
			m.snap.HostTierAvailable = false
			return m, nil
		}
		m.snap = service.MergeHostTier(m.snap, msg.rep, time.Now())
		m.recordMemSamples()
		m.feedFleetViews()
		m.layout()
		return m, nil

	case autoTickMsg:
		if !m.autoOn {
			return m, nil // disarmed - stop re-arming
		}
		if m.idleForAuto() {
			return m, tea.Batch(m.reloadCurrent(), autoTickCmd(m.autoEvery))
		}
		return m, autoTickCmd(m.autoEvery)

	case settingsMsg:
		m.loading = false
		m.settings.setSnapshot(msg.snap)
		m.status, m.stErr = "capacity policy", false
		return m, nil

	case groupsMsg:
		m.loading = false
		rows := msg.rows
		if len(m.orgVisible) > 0 {
			rows = rows[:0:0]
			for _, g := range msg.rows {
				if m.orgVisible[g.Org] {
					rows = append(rows, g)
				}
			}
		}
		m.groups.setRows(rows)
		m.setLoadStatus(len(rows), "group", msg.err)
		return m, nil

	case healthMsg:
		m.loading = false
		reps := msg.reports
		if len(m.orgVisible) > 0 {
			reps = reps[:0:0]
			for _, r := range msg.reports {
				if m.orgVisible[r.Org] {
					reps = append(reps, r)
				}
			}
		}
		m.health.setReports(reps, msg.host)
		m.setLoadStatus(len(reps), "org", msg.err)
		return m, nil

	case actionMsg:
		if msg.err != nil {
			m.status, m.stErr = msg.err.Error(), true
			return m, nil
		}
		m.status, m.stErr = msg.summary, false
		return m, m.reloadCurrent()

	case progressMsg:
		if msg.ev.Total > 0 {
			m.createTotal = msg.ev.Total
		}
		m.createDone = msg.ev.Index
		m.createMsg = msg.ev.Message
		return m, waitCreateCmd(m.createCh, m.createResCh, m.createOrg, m.createNoun)

	case createDoneMsg:
		m.creating = false
		m.createCh, m.createResCh = nil, nil
		if msg.err != nil {
			m.status, m.stErr = msg.err.Error(), true
			return m, nil
		}
		m.status, m.stErr = msg.summary, false
		return m, m.reloadCurrent()

	case opProgressMsg:
		if msg.ev.Total > 0 {
			m.op.total = msg.ev.Total
		}
		m.op.done = msg.ev.Index
		if msg.ev.Message != "" {
			m.op.msg = msg.ev.Message
		}
		return m, waitOpCmd(m.op.ch, m.op.resCh)

	case opDoneMsg:
		m.op.running = false
		m.op.items = msg.items
		m.op.err = msg.err
		m.op.ch, m.op.resCh = nil, nil
		return m, nil

	case previewMsg:
		m.loading = false
		if msg.err != nil {
			m.status, m.stErr = msg.err.Error(), true
			return m, nil
		}
		m.preview = newPreview(m.theme, msg.title, msg.note, msg.lines)
		m.previewOpen = true
		spec := msg.spec
		m.pendingOp = &spec
		m.pendingTyped = msg.typedToken
		m.pendingTyped2 = msg.typedToken2
		return m, nil

	case groupSavedMsg:
		if msg.err != nil {
			m.status, m.stErr = msg.err.Error(), true
			return m, nil
		}
		m.status, m.stErr = msg.summary, false
		if msg.openPicker {
			m.picker = newRepoPicker(m.theme, msg.org, msg.id, msg.name)
			m.pickerOpen = true
			return m, loadReposCmd(m.ctx, m.mgr, msg.org, msg.id)
		}
		m.loading = true
		return m, loadGroupsCmd(m.ctx, m.mgr, m.orgFilter)

	case reposLoadedMsg:
		if msg.err != nil {
			m.picker.loadErr = msg.err
			m.picker.loading = false
			return m, nil
		}
		m.picker.setData(msg.all, msg.current)
		return m, nil

	case jobLookupMsg:
		if !m.infoOpen || m.infoRunner == nil {
			return m, nil // info closed before the lookup returned
		}
		switch {
		case msg.err != nil:
			m.infoJobState = "error"
		case msg.found:
			m.infoJob, m.infoJobState = msg.job, "found"
		default:
			m.infoJobState = "none"
		}
		m.rebuildRunnerInfo()
		return m, nil

	case groupReposInfoMsg:
		if !m.infoOpen || m.infoGroup == nil {
			return m, nil
		}
		m.infoGroupRepos = msg.repos
		m.infoGroupReposState = "loaded"
		m.rebuildGroupInfo()
		return m, nil

	case runnerGroupsMsg:
		m.loading = false
		if msg.err != nil {
			m.status, m.stErr = msg.err.Error(), true
			return m, nil
		}
		m.status = ""
		m.runnerEdit = newRunnerEditForm(msg.runner, msg.groups)
		return m, m.runnerEdit.form.Init()

	case backupsMsg:
		m.loading = false
		if msg.err != nil {
			m.status, m.stErr = "list backups: "+msg.err.Error(), true
			return m, nil
		}
		if len(msg.backups) == 0 {
			m.status, m.stErr = "no config backups found in "+msg.dir+" (create one with the Back up card first)", false
			return m, nil
		}
		m.restore = restorePicker{theme: m.theme, backups: msg.backups}
		m.restoreOpen = true
		return m, nil

	case reloadMsg:
		m.loading = false
		if msg.err != nil {
			m.status, m.stErr = msg.err.Error(), true
			return m, nil
		}
		m.status, m.stErr = msg.note, msg.warn
		// The config changed under us (new org / restored dir): drop the cached fleet
		// snapshot so the next fleet view reloads against the reloaded Manager.
		m.fleetLoaded = false
		return m, m.reloadCurrent()
	}

	return m, nil
}

// updateGroupForm drives the group create/edit form. On completion it creates or
// updates the group (chaining into the repo picker for "selected" visibility); on
// abort it just closes.
func (m Model) updateGroupForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
		m.help.SetWidth(ws.Width)
		m.layout()
	}
	fm, cmd := m.groupForm.form.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		m.groupForm.form = f
	}
	switch m.groupForm.form.State {
	case huh.StateCompleted:
		gf := m.groupForm
		m.groupForm = nil
		m.loading = true
		m.status = "saving group…"
		if gf.editing {
			return m, updateGroupCmd(m.ctx, m.mgr, gf.org, gf.id, gf.name, gf.visibility)
		}
		return m, createGroupCmd(m.ctx, m.mgr, gf.org, gf.name, gf.visibility)
	case huh.StateAborted:
		m.groupForm = nil
		m.status, m.stErr = "group edit cancelled", false
		return m, nil
	}
	return m, cmd
}

func (m Model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// While a create is streaming, only quit is honored.
	if m.creating {
		if key.Matches(msg, m.keys.Quit) {
			return m, tea.Quit
		}
		return m, nil
	}

	// The operation result panel: while running only quit; once done, enter/esc
	// dismisses and reloads the active tab (the create panel's behavior, generalized).
	if m.op.open {
		if m.op.running {
			if key.Matches(msg, m.keys.Quit) {
				return m, tea.Quit
			}
			return m, nil
		}
		switch {
		case key.Matches(msg, m.keys.Enter), key.Matches(msg, m.keys.Esc), key.Matches(msg, m.keys.Quit):
			m.op = opState{}
			m.clearSelections()
			return m, m.reloadCurrent()
		}
		return m, nil
	}

	// The org-filter modal captures all keys while open.
	if m.orgPickOpen {
		return m.orgFilterKey(msg)
	}

	// The Information panel captures keys: i/esc/q close, everything else scrolls.
	if m.infoOpen {
		switch {
		case msg.String() == "ctrl+c":
			return m, tea.Quit
		case key.Matches(msg, m.keys.Info), key.Matches(msg, m.keys.Esc), msg.String() == "q":
			m.infoOpen = false
			return m, nil
		}
		var cmd tea.Cmd
		m.info, cmd = m.info.update(msg)
		return m, cmd
	}

	// The dry-run-preview modal: Apply / Cancel (defaults Cancel). When the op
	// carries a typed token, Apply escalates to the typed-confirm gate.
	if m.previewOpen {
		switch {
		case key.Matches(msg, m.keys.Quit), key.Matches(msg, m.keys.Esc), msg.String() == "n":
			m.previewOpen, m.pendingOp, m.pendingTyped, m.pendingTyped2 = false, nil, "", ""
			m.status, m.stErr = "cancelled", false
			return m, nil
		case msg.String() == "left", msg.String() == "right", key.Matches(msg, m.keys.Tab), key.Matches(msg, m.keys.ShiftTab):
			m.preview.toggle()
			return m, nil
		case msg.String() == "y":
			return m.applyPreview()
		case key.Matches(msg, m.keys.Enter):
			if m.preview.apply {
				return m.applyPreview()
			}
			m.previewOpen, m.pendingOp, m.pendingTyped, m.pendingTyped2 = false, nil, "", ""
			return m, nil
		}
		return m, nil
	}

	// The restore backup picker captures all keys while open.
	if m.restoreOpen {
		switch {
		case key.Matches(msg, m.keys.Esc), key.Matches(msg, m.keys.Quit):
			m.restoreOpen = false
			m.status, m.stErr = "restore cancelled", false
			return m, nil
		case key.Matches(msg, m.keys.Up):
			m.restore.move(-1)
			return m, nil
		case key.Matches(msg, m.keys.Down):
			m.restore.move(1)
			return m, nil
		case key.Matches(msg, m.keys.Enter):
			b, ok := m.restore.selected()
			if !ok {
				return m, nil
			}
			m.restoreOpen = false
			archive := b.Path
			m.modal = newConfirm(m.theme, "Restore config?",
				fmt.Sprintf("Overwrite the current config dir with %q? config.yaml, secrets, and per-org keys are replaced (back up first if unsure).", b.Name))
			m.onConfirm = func() tea.Cmd { return restoreCmd(archive, m.mgr) }
			m.modalOpen = true
			return m, nil
		}
		return m, nil
	}

	// The repo-access picker captures all keys while open: esc cancels, enter saves
	// the chosen set, everything else drives the search box / candidate navigation.
	if m.pickerOpen {
		switch {
		case key.Matches(msg, m.keys.Esc):
			m.pickerOpen = false
			m.status, m.stErr = "repo assignment cancelled", false
			return m, nil
		case key.Matches(msg, m.keys.Enter):
			if m.picker.loading {
				return m, nil
			}
			org, gid, ids := m.picker.org, m.picker.groupID, m.picker.selectedIDs()
			m.pickerOpen = false
			m.loading = true
			m.status = "saving repo access…"
			return m, setGroupReposCmd(m.ctx, m.mgr, org, gid, ids)
		default:
			var cmd tea.Cmd
			m.picker, cmd = m.picker.update(msg)
			return m, cmd
		}
	}

	// The typed-confirm modal captures all keys while open.
	if m.typedOpen {
		switch {
		case key.Matches(msg, m.keys.Esc):
			m.typedOpen, m.pendingOp, m.pendingTyped, m.pendingTyped2 = false, nil, "", ""
			m.status, m.stErr = "cancelled", false
			return m, nil
		case key.Matches(msg, m.keys.Enter):
			if m.typed.matched() {
				// A chained second token (purge) opens another typed-confirm instead of
				// running the op; only an empty pendingTyped2 arms the operation.
				if m.pendingTyped2 != "" {
					tok := m.pendingTyped2
					m.pendingTyped2 = ""
					m.typed = newTypedConfirm(m.theme, "Confirm PURGE",
						"This ALSO deletes /etc/srm (config + App keys) after a backup. Type PURGE exactly to proceed.",
						tok, tok)
					return m, nil
				}
				return m.runPendingOp()
			}
			return m, nil
		default:
			var cmd tea.Cmd
			m.typed, cmd = m.typed.update(msg)
			return m, cmd
		}
	}

	// The active view's filter captures keystrokes while focused.
	if m.activeFiltering() {
		switch {
		case key.Matches(msg, m.keys.Esc):
			m.stopFilter(true)
		case key.Matches(msg, m.keys.Enter):
			m.stopFilter(false)
		default:
			nm, cmd := m.updateActiveFilter(msg)
			return nm, cmd
		}
		return m, nil
	}

	// The yes/no confirm modal captures all keys while open.
	if m.modalOpen {
		switch {
		case key.Matches(msg, m.keys.Quit), key.Matches(msg, m.keys.Esc):
			m.modalOpen, m.pendingOp, m.onConfirm = false, nil, nil
		case msg.String() == "left", msg.String() == "right", key.Matches(msg, m.keys.Tab), key.Matches(msg, m.keys.ShiftTab):
			m.modal.toggle()
		case msg.String() == "y":
			return m.runConfirmed()
		case msg.String() == "n":
			m.modalOpen, m.pendingOp, m.onConfirm = false, nil, nil
		case key.Matches(msg, m.keys.Enter):
			if m.modal.yes {
				return m.runConfirmed()
			}
			m.modalOpen, m.pendingOp, m.onConfirm = false, nil, nil
		}
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.layout()
		return m, nil
	case key.Matches(msg, m.keys.Tab):
		m.tab = (m.tab + 1) % tab(len(tabNames))
		return m, m.enterTab()
	case key.Matches(msg, m.keys.ShiftTab):
		m.tab = (m.tab + tab(len(tabNames)) - 1) % tab(len(tabNames))
		return m, m.enterTab()
	case key.Matches(msg, m.keys.Org):
		m.orgPick = newOrgFilterModal(m.theme, m.mgr.OrgNames(), m.orgVisible)
		m.orgPickOpen = true
		return m, nil
	case key.Matches(msg, m.keys.Refresh):
		m.status = ""
		return m, m.reloadCurrent()
	case key.Matches(msg, m.keys.Auto):
		return m.toggleAuto()
	case key.Matches(msg, m.keys.Enter):
		return m.onEnter()
	case key.Matches(msg, m.keys.Info):
		return m.openInfo()
	case key.Matches(msg, m.keys.Esc):
		return m.onEsc()
	case key.Matches(msg, m.keys.Fix):
		return m.startFix()
	case key.Matches(msg, m.keys.Reap):
		return m.startReap()
	case key.Matches(msg, m.keys.Upgrade):
		return m.startUpgrade()
	case key.Matches(msg, m.keys.Rollback):
		return m.startRollback()
	case key.Matches(msg, m.keys.Refresh2):
		return m.startRefreshUnits()
	case key.Matches(msg, m.keys.Prune):
		return m.startPrune()
	case key.Matches(msg, m.keys.Provision):
		return m.startProvision()
	case key.Matches(msg, m.keys.Recreate):
		return m.startRecreate()
	case key.Matches(msg, m.keys.Select):
		if !m.selectableTab() {
			m.status, m.stErr = "multi-select is available on the Persistent and Ephemeral views", false
			return m, nil
		}
		m.toggleSelect()
		return m, nil
	case key.Matches(msg, m.keys.SelectAll):
		if !m.selectableTab() {
			m.status, m.stErr = "select-all is available on the Persistent and Ephemeral views", false
			return m, nil
		}
		m.selectAllVisible()
		return m, nil
	case key.Matches(msg, m.keys.Dismiss):
		m.bannerDismissed = true
		m.layout()
		return m, nil
	case key.Matches(msg, m.keys.Filter):
		if m.filterableTab() {
			return m, m.startFilter()
		}
		m.status, m.stErr = "filter is not available on this view", false
		return m, nil
	case key.Matches(msg, m.keys.New):
		switch m.tab {
		case tabPersistent:
			m.form = newCreateForm(m.mgr.OrgNames())
			m.formOpen = true
			return m, m.form.form.Init()
		case tabEphemeral:
			m.form = newEphemeralForm(m.mgr.OrgNames())
			m.formOpen = true
			return m, m.form.form.Init()
		case tabGroups:
			m.groupForm = newGroupForm(m.mgr.OrgNames())
			return m, m.groupForm.form.Init()
		default:
			m.status, m.stErr = "create is available on the Persistent, Ephemeral, and Groups views", false
			return m, nil
		}
	case key.Matches(msg, m.keys.Delete):
		return m.askDelete()
	case key.Matches(msg, m.keys.Edit):
		switch m.tab {
		case tabSettings:
			m.setForm = newSettingsForm(m.mgr.ResourceSettings())
			return m, m.setForm.form.Init()
		case tabGroups:
			return m.startEditGroup()
		case tabHealth:
			return m.startEditRetention()
		case tabPersistent:
			return m.startEditRunner()
		case tabEphemeral:
			m.status, m.stErr = "ephemeral slots take labels/group from their mint params - use recreate (c) to change them", false
			return m, nil
		default:
			m.status, m.stErr = "edit is available on Persistent, Groups, Health, and Settings", false
			return m, nil
		}
	}

	// Delegate movement to the active view's table.
	var cmd tea.Cmd
	switch m.tab {
	case tabEphemeral:
		m.ephemeral, cmd = m.ephemeral.update(msg)
	case tabGroups:
		m.groups, cmd = m.groups.update(msg)
	case tabDrift:
		m.drift, cmd = m.drift.update(msg)
	case tabSettings:
		// up/down drive the Lifecycle menu in the right-hand panel.
		switch {
		case key.Matches(msg, m.keys.Up):
			m.lifecycle.move(-1)
		case key.Matches(msg, m.keys.Down):
			m.lifecycle.move(1)
		}
	case tabHealth:
		// up/down select the org card that e/u act on; PgUp/PgDn scroll the host panel.
		switch {
		case key.Matches(msg, m.keys.Up):
			m.health.move(-1)
		case key.Matches(msg, m.keys.Down):
			m.health.move(1)
		case key.Matches(msg, m.keys.PageUp):
			m.health.pageHost(-1)
		case key.Matches(msg, m.keys.PageDown):
			m.health.pageHost(1)
		}
	default:
		m.runners, cmd = m.runners.update(msg)
	}
	return m, cmd
}

// actionOrg is the org a per-org action targets: the highlighted Health card when
// on the Health tab, else the load scope ("" = all).
func (m Model) actionOrg() string {
	if m.tab == tabHealth {
		if o := m.health.selectedOrg(); o != "" {
			return o
		}
	}
	return m.orgFilter
}

// visibleRunners / visibleSlots drop rows whose org is hidden by the org filter.
func (m Model) visibleRunners(rs []service.FusedRunner) []service.FusedRunner {
	if len(m.orgVisible) == 0 {
		return rs
	}
	out := make([]service.FusedRunner, 0, len(rs))
	for _, r := range rs {
		if m.orgVisible[r.Org] {
			out = append(out, r)
		}
	}
	return out
}

func (m Model) visibleSlots(ss []service.FusedSlot) []service.FusedSlot {
	if len(m.orgVisible) == 0 {
		return ss
	}
	out := make([]service.FusedSlot, 0, len(ss))
	for _, s := range ss {
		if m.orgVisible[s.Org] {
			out = append(out, s)
		}
	}
	return out
}

// memHistoryMax bounds the per-runner memory ring (last N host-tier samples).
const memHistoryMax = 30

// memKeyRunner / memKeySlot namespace the memHistory keys so a runner and a slot can
// never collide (org + name, each in its own "r"/"s" space).
func memKeyRunner(r service.FusedRunner) string { return "r\x00" + r.Org + "\x00" + r.Runner.Name }
func memKeySlot(s service.FusedSlot) string      { return "s\x00" + s.Org + "\x00" + s.Slot }

// memHistoryFor returns the recorded samples for a key (nil when none / not yet
// sampled), safe on a nil map.
func (m Model) memHistoryFor(key string) []int64 { return m.memHistory[key] }

// recordMemSamples appends the current live cgroup-memory reading for every audited
// runner and host-local slot to its bounded ring, then prunes rings for keys no
// longer in the snapshot (destroyed runners/slots). Called after a host-tier merge,
// the only point MemCur is real; an unknown (-1) reading marks the key live but does
// not push a bogus zero.
func (m *Model) recordMemSamples() {
	if m.memHistory == nil {
		m.memHistory = map[string][]int64{}
	}
	live := map[string]bool{}
	push := func(key string, v int64) {
		live[key] = true
		if v < 0 {
			return
		}
		h := append(m.memHistory[key], v)
		if len(h) > memHistoryMax {
			h = h[len(h)-memHistoryMax:]
		}
		m.memHistory[key] = h
	}
	for _, r := range m.snap.Runners {
		if r.HostKnown {
			push(memKeyRunner(r), r.MemCur)
		}
	}
	for _, s := range m.snap.Slots {
		push(memKeySlot(s), s.MemCur)
	}
	for k := range m.memHistory {
		if !live[k] {
			delete(m.memHistory, k)
		}
	}
}

// feedFleetViews pushes the snapshot (org-filtered) into the runner/slot/drift views.
func (m *Model) feedFleetViews() {
	m.runners.setRows(m.visibleRunners(m.snap.Runners))
	m.ephemeral.setRows(m.visibleSlots(m.snap.Slots))
	ds := m.snap
	ds.Runners = m.visibleRunners(m.snap.Runners)
	ds.Slots = m.visibleSlots(m.snap.Slots)
	m.drift.setRows(ds)
}

// toggleAuto arms/disarms auto-refresh; arming kicks the first tick.
func (m Model) toggleAuto() (tea.Model, tea.Cmd) {
	m.autoOn = !m.autoOn
	if m.autoOn {
		m.status, m.stErr = fmt.Sprintf("auto-refresh on (%s)", m.autoEvery), false
		return m, autoTickCmd(m.autoEvery)
	}
	m.status, m.stErr = "auto-refresh off", false
	return m, nil
}

// onEnter dispatches the enter key: Drift jumps to the row's home tab; Groups opens
// the repo-access picker; Settings runs the focused Lifecycle card; the runner list
// tabs open the Information panel.
func (m Model) onEnter() (tea.Model, tea.Cmd) {
	switch m.tab {
	case tabDrift:
		return m.jumpFromDrift()
	case tabGroups:
		return m.manageRepos()
	case tabSettings:
		return m.runLifecycle() // the right-hand Lifecycle panel is the actionable one
	default:
		return m.openInfo()
	}
}

// runLifecycle dispatches the focused Lifecycle card (the Settings tab's right panel).
func (m Model) runLifecycle() (tea.Model, tea.Cmd) {
	card := m.lifecycle.selected()
	if card.hostOp && !m.requireHost(card.title) {
		return m, nil
	}
	switch card.action {
	case lifeBackup:
		return m.confirmOp("Back up config?",
			"Snapshot config.yaml + secrets.age + per-org keys to a timestamped tarball.",
			backupOp())
	case lifeProvision:
		return m.startProvision()
	case lifeUninstall:
		// Step 1: collect scope + Keep*/Force/Purge; the form's completion runs the
		// dry-run blast-radius preview, which then escalates the destructive gates.
		m.uninstallForm = newUninstallForm(m.mgr.OrgNames(), m.orgFilter)
		return m, m.uninstallForm.form.Init()
	case lifeOnboard:
		// In-TUI onboarding: the same org-credential form as `srm init`, then a
		// save + reload + auth check. The .pem placeholder is seeded from the config dir.
		m.onboardForm = newOnboardForm(filepath.Dir(m.mgr.ConfigPath()))
		return m, m.onboardForm.form.Init()
	case lifeRestore:
		m.loading = true
		m.status = "scanning for config backups…"
		return m, loadBackupsCmd(m.mgr)
	}
	return m, nil
}

// startEditRetention opens the retention edit form for the org filter's org. It
// needs a single org in scope (retention is org-level) and the value loaded.
func (m Model) startEditRetention() (tea.Model, tea.Cmd) {
	org := m.health.selectedOrg()
	if org == "" {
		m.status, m.stErr = "select an org card first (↑/↓)", false
		return m, nil
	}
	cur, max, ok := m.health.retentionFor(org)
	if !ok {
		m.status, m.stErr = "retention is unavailable for this org (the App may lack the Administration permission - see GITHUB_APP_SETUP.md)", true
		return m, nil
	}
	m.retForm = newRetentionForm(org, cur, max)
	return m, m.retForm.form.Init()
}

// startEditRunner opens the group-move form for the selected persistent runner.
func (m Model) startEditRunner() (tea.Model, tea.Cmd) {
	r, ok := m.runners.selected()
	if !ok {
		return m, nil
	}
	// Load the org's groups first so the edit form is a Select of real groups.
	m.loading = true
	m.status = "loading groups…"
	return m, loadRunnerGroupsCmd(m.ctx, m.mgr, r)
}

// startEditGroup opens the edit form for the selected group (refuses Default).
func (m Model) startEditGroup() (tea.Model, tea.Cmd) {
	g, ok := m.groups.selected()
	if !ok {
		return m, nil
	}
	if g.Group.ID == service.DefaultGroupID {
		m.status, m.stErr = "the Default group cannot be edited", true
		return m, nil
	}
	m.groupForm = newGroupEditForm(g)
	return m, m.groupForm.form.Init()
}

// manageRepos opens the repo-access picker for the selected group. Repo access only
// applies to "selected" visibility, so it nudges the operator to set that first.
func (m Model) manageRepos() (tea.Model, tea.Cmd) {
	g, ok := m.groups.selected()
	if !ok {
		return m, nil
	}
	if g.Group.Visibility != "selected" {
		m.status, m.stErr = "repo access applies to 'selected' visibility - press e and set it first", false
		return m, nil
	}
	m.picker = newRepoPicker(m.theme, g.Org, g.Group.ID, g.Group.Name)
	m.pickerOpen = true
	m.status = ""
	return m, loadReposCmd(m.ctx, m.mgr, g.Org, g.Group.ID)
}

// openInfo opens the full-screen Information panel for the selected row (Persistent
// runner, Ephemeral slot, or Group). For a busy runner it kicks an async current-job
// lookup; for a "selected" group it loads the assigned repos. No-op on tabs without
// a selectable subject (a status hint tells the operator where i works).
func (m Model) openInfo() (tea.Model, tea.Cmd) {
	m.infoRunner, m.infoGroup = nil, nil
	switch m.tab {
	case tabPersistent:
		r, ok := m.runners.selected()
		if !ok {
			return m, nil
		}
		return m, m.openRunnerInfo(r)
	case tabEphemeral:
		s, ok := m.ephemeral.selected()
		if !ok {
			return m, nil
		}
		m.openSlotInfo(s)
		return m, nil
	case tabDrift:
		return m.openDriftInfo()
	case tabGroups:
		g, ok := m.groups.selected()
		if !ok {
			return m, nil
		}
		m.infoGroup = &g
		m.infoGroupRepos, m.infoGroupReposState = nil, ""
		var cmd tea.Cmd
		if g.Group.Visibility == "selected" {
			m.infoGroupReposState = "looking"
			cmd = groupReposInfoCmd(m.ctx, m.mgr, g.Org, g.Group.ID)
		}
		m.rebuildGroupInfo()
		m.infoOpen = true
		m.layout()
		return m, cmd
	case tabHealth:
		rep, ok := m.health.selected()
		if !ok {
			return m, nil
		}
		title, body := healthInfo(m.theme, rep, m.health.host)
		m.info.set(title, body)
		m.infoOpen = true
		m.layout()
		return m, nil
	default:
		m.status, m.stErr = "press i on a runner, slot, group, or org", false
		return m, nil
	}
}

// openRunnerInfo stores a runner as the info subject and opens the panel, firing
// the async current-job lookup when the runner is busy. Returns the lookup cmd
// (nil when idle). Shared by the Persistent and Drift tabs.
func (m *Model) openRunnerInfo(r service.FusedRunner) tea.Cmd {
	m.infoRunner = &r
	m.infoJob, m.infoJobState = nil, ""
	var cmd tea.Cmd
	if r.Runner.Busy {
		m.infoJobState = "looking"
		cmd = jobLookupCmd(m.ctx, m.mgr, r.Org, r.Runner.Name)
	}
	m.rebuildRunnerInfo()
	m.infoOpen = true
	m.layout()
	return cmd
}

// openSlotInfo stores an ephemeral slot as the info subject and opens the panel.
// Shared by the Ephemeral and Drift tabs.
func (m *Model) openSlotInfo(s service.FusedSlot) {
	title, body := slotInfo(m.theme, s, m.memHistoryFor(memKeySlot(s)))
	m.info.set(title, body)
	m.infoOpen = true
	m.layout()
}

// openDriftInfo resolves the selected drift row back to its runner/slot in the
// loaded snapshot and opens the matching Information panel (i on Drift mirrors i
// on the row's home tab). A no-op if the row is no longer in the snapshot.
func (m Model) openDriftInfo() (tea.Model, tea.Cmd) {
	e, ok := m.drift.selected()
	if !ok {
		return m, nil
	}
	if e.isSlot {
		slot := strings.TrimPrefix(e.name, "slot ")
		for _, s := range m.snap.Slots {
			if s.Org == e.org && s.Slot == slot {
				m.openSlotInfo(s)
				return m, nil
			}
		}
		return m, nil
	}
	for _, r := range m.snap.Runners {
		if r.Org == e.org && r.Runner.Name == e.name {
			return m, m.openRunnerInfo(r)
		}
	}
	return m, nil
}

// rebuildRunnerInfo / rebuildGroupInfo re-render the info content from the stored
// subject + the latest async data (current job / assigned repos).
func (m *Model) rebuildRunnerInfo() {
	if m.infoRunner == nil {
		return
	}
	title, body := runnerInfo(m.theme, *m.infoRunner, m.memHistoryFor(memKeyRunner(*m.infoRunner)), m.infoJob, m.infoJobState)
	m.info.set(title, body)
}

func (m *Model) rebuildGroupInfo() {
	if m.infoGroup == nil {
		return
	}
	title, body := groupInfo(m.theme, *m.infoGroup, m.visibleRunners(m.snap.Runners), m.visibleSlots(m.snap.Slots), m.infoGroupRepos, m.infoGroupReposState)
	m.info.set(title, body)
}

// jumpFromDrift switches to the home tab of the selected drift row and focuses it.
func (m Model) jumpFromDrift() (tea.Model, tea.Cmd) {
	e, ok := m.drift.selected()
	if !ok {
		return m, nil
	}
	if e.isSlot {
		slot := strings.TrimPrefix(e.name, "slot ")
		m.tab = tabEphemeral
		m.ephemeral.focusKey(e.org + "\x00" + slot)
	} else {
		m.tab = tabPersistent
		m.runners.focusKey(e.org + "\x00" + e.name)
	}
	m.layout()
	return m, nil
}

// startFix launches the reconcile-fix flow: a dry-run preview, then Apply. Org-wide
// (the current org filter). Gated on host capability.
func (m Model) startFix() (tea.Model, tea.Cmd) {
	if !fleetTab(m.tab) {
		return m, nil
	}
	if !m.hostCapable {
		m.status, m.stErr = "drift repair needs root on the runner host", true
		return m, nil
	}
	m.loading = true
	m.status = "planning fix…"
	return m, planFixCmd(m.ctx, m.mgr, m.orgFilter, m.theme)
}

// startReap launches the ephemeral-ghost reap flow: a dry-run preview, then Apply.
func (m Model) startReap() (tea.Model, tea.Cmd) {
	if !fleetTab(m.tab) {
		return m, nil
	}
	if !m.hostCapable {
		m.status, m.stErr = "reaping ghosts needs root on the runner host", true
		return m, nil
	}
	m.loading = true
	m.status = "planning reap…"
	return m, planReapCmd(m.ctx, m.mgr, m.orgFilter, m.theme)
}

// scopeOf renders an org scope for human-facing copy ("" = all orgs).
func scopeOf(org string) string {
	if org == "" {
		return "all orgs"
	}
	return org
}

// requireHost gates a host-mutating op: returns false (and sets a status reason)
// when this is not an elevated on-host session.
func (m *Model) requireHost(action string) bool {
	if !m.hostCapable {
		m.status, m.stErr = action+" needs root on the runner host (remote / non-elevated session)", true
		return false
	}
	return true
}

// confirmOp opens the yes/no confirm modal guarding a pending operation.
func (m Model) confirmOp(title, message string, spec opSpec) (tea.Model, tea.Cmd) {
	m.modal = newConfirm(m.theme, title, message)
	sp := spec
	m.pendingOp = &sp
	m.modalOpen = true
	return m, nil
}

// startUpgrade: dry-run preview, then a streamed serial upgrade. Scoped to the
// persistent-runner selection when one is active, else the whole org (host-wide,
// which the preview's Apply then gates behind a typed hostname confirm).
func (m Model) startUpgrade() (tea.Model, tea.Cmd) {
	if !m.requireHost("upgrade") {
		return m, nil
	}
	// Collect --to-version / --force first; the form's completion runs the dry-run
	// preview with the same scope startUpgrade would have used.
	m.upgradeForm = newUpgradeForm()
	return m, m.upgradeForm.form.Init()
}

// startRollback: confirm, then restore each runner to its previous version. Scoped
// to the persistent-runner selection when one is active.
func (m Model) startRollback() (tea.Model, tea.Cmd) {
	if !m.requireHost("rollback") {
		return m, nil
	}
	only := m.selectionOnly()
	return m.confirmOp("Roll back agents?",
		fmt.Sprintf("Restore %s to its PREVIOUS agent version.", upgradeScope(m.actionOrg(), only)),
		rollbackOp(m.actionOrg(), only))
}

// startRefreshUnits: confirm, then re-apply the drop-in + restart idle runners.
// Scoped to the persistent-runner selection when one is active.
func (m Model) startRefreshUnits() (tea.Model, tea.Cmd) {
	if !m.requireHost("refresh") {
		return m, nil
	}
	only := m.selectionOnly()
	return m.confirmOp("Refresh units?",
		fmt.Sprintf("Re-apply the systemd drop-in and restart idle runners in %s (busy ones are skipped).", upgradeScope(m.actionOrg(), only)),
		refreshOp(m.actionOrg(), only))
}

// selectionOnly is the persistent-runner selection as an upgrade/refresh filter
// (nil when nothing is selected, meaning the whole org scope).
func (m Model) selectionOnly() map[string]bool {
	return service.OnlyRunners(m.runners.selectedRunners())
}

// upgradeScope renders the human scope for upgrade/rollback/refresh copy: the
// selected-runner count when a selection is active, else the org scope.
func upgradeScope(org string, only map[string]bool) string {
	if n := len(only); n > 0 {
		return fmt.Sprintf("%d selected runner(s)", n)
	}
	return "every recorded runner in " + scopeOf(org)
}

// startPrune: dry-run preview, then evict aged dep-cache entries.
func (m Model) startPrune() (tea.Model, tea.Cmd) {
	if !m.requireHost("prune") {
		return m, nil
	}
	m.loading = true
	m.status = "planning prune…"
	return m, planPruneCmd(m.ctx, m.mgr, m.mgr.CacheRetentionDaysOrDefault())
}

// startProvision: host-drift preview, then install the host dependency manifest.
func (m Model) startProvision() (tea.Model, tea.Cmd) {
	if !m.requireHost("provision") {
		return m, nil
	}
	m.loading = true
	m.status = "checking host drift…"
	return m, planProvisionCmd(m.ctx, m.mgr, m.theme)
}

// startRecreate: confirm, then destroy + recreate the selection (or cursor row)
// with the same config. Persistent and Ephemeral only.
func (m Model) startRecreate() (tea.Model, tea.Cmd) {
	if !m.requireHost("recreate") {
		return m, nil
	}
	switch m.tab {
	case tabPersistent:
		targets := m.runners.selectedRunners()
		if len(targets) == 0 {
			if r, ok := m.runners.selected(); ok {
				targets = []service.FusedRunner{r}
			}
		}
		if len(targets) == 0 {
			return m, nil
		}
		return m.confirmOp("Recreate runner(s)?",
			fmt.Sprintf("Destroy and re-create %d runner(s) with the SAME name, labels, and group.", len(targets)),
			recreateRunnersOp(targets))
	case tabEphemeral:
		targets := m.ephemeral.selectedSlots()
		if len(targets) == 0 {
			if s, ok := m.ephemeral.selected(); ok {
				targets = []service.FusedSlot{s}
			}
		}
		if len(targets) == 0 {
			return m, nil
		}
		return m.confirmOp("Recreate slot(s)?",
			fmt.Sprintf("Destroy and re-create %d slot lane(s) with the SAME slot id, labels, and group.", len(targets)),
			recreateSlotsOp(targets))
	default:
		m.status, m.stErr = "recreate is available on the Persistent and Ephemeral views", false
		return m, nil
	}
}

// onEsc closes the Information panel, else clears a multi-selection, else nothing.
func (m Model) onEsc() (tea.Model, tea.Cmd) {
	if m.infoOpen {
		m.infoOpen = false
		return m, nil
	}
	if m.selectionCount() > 0 {
		m.clearSelections()
		return m, nil
	}
	return m, nil
}

// idleForAuto reports whether an auto-refresh tick may load now: nothing modal,
// no op in flight, not filtering, and not already loading.
func (m Model) idleForAuto() bool {
	return !m.loading && !m.modalOpen && !m.typedOpen && !m.previewOpen && !m.formOpen && !m.infoOpen &&
		m.setForm == nil && m.retForm == nil && m.groupForm == nil && m.runnerEdit == nil && m.upgradeForm == nil && m.uninstallForm == nil && m.onboardForm == nil && !m.restoreOpen && !m.pickerOpen && !m.orgPickOpen && !m.op.open && !m.creating && !m.activeFiltering()
}

// runConfirmed runs the yes/no-confirmed action: a pending op (through the op
// runner) or the legacy onConfirm command.
func (m Model) runConfirmed() (tea.Model, tea.Cmd) {
	m.modalOpen = false
	if m.pendingOp != nil {
		return m.runPendingOp()
	}
	if m.onConfirm == nil {
		return m, nil
	}
	cmd := m.onConfirm()
	m.onConfirm = nil
	m.loading = true
	m.status = "working…"
	return m, cmd
}

// applyPreview handles the preview's Apply: a typed-token op escalates to the
// typed-confirm gate; otherwise the op launches immediately.
func (m Model) applyPreview() (tea.Model, tea.Cmd) {
	m.previewOpen = false
	if m.pendingTyped != "" {
		token := m.pendingTyped
		m.typed = newTypedConfirm(m.theme, "Confirm destructive action",
			fmt.Sprintf("This is irreversible. Type the host name %q exactly to proceed.", token),
			token, token)
		m.typedOpen = true
		return m, nil
	}
	return m.runPendingOp()
}

// runPendingOp launches the gated operation through the op-runner.
func (m Model) runPendingOp() (tea.Model, tea.Cmd) {
	m.modalOpen, m.typedOpen = false, false
	m.pendingTyped, m.pendingTyped2 = "", ""
	spec := *m.pendingOp
	m.pendingOp = nil
	m.op = startOp(m.ctx, m.mgr, spec)
	return m, tea.Batch(m.spin.Tick, waitOpCmd(m.op.ch, m.op.resCh))
}

// askDelete opens the confirm modal for the highlighted item. On the Persistent
// view a local runner is destroyed (host teardown + deregister) and a remote one
// is only deregistered; on the Ephemeral view a slot lane is torn down by slot id.
func (m Model) askDelete() (tea.Model, tea.Cmd) {
	switch m.tab {
	case tabPersistent:
		if sel := m.runners.selectedRunners(); len(sel) > 0 {
			return m.confirmOp("Destroy selected runners?",
				fmt.Sprintf("Destroy/deregister %d selected runner(s). Local ones are torn down on this host; remote ones are only deregistered.", len(sel)),
				destroyRunnersOp(sel))
		}
		return m.askDeleteRunner()
	case tabEphemeral:
		if sel := m.ephemeral.selectedSlots(); len(sel) > 0 {
			return m.confirmOp("Destroy selected slots?",
				fmt.Sprintf("Tear down %d selected slot lane(s) on this host.", len(sel)),
				destroySlotsOp(sel))
		}
		return m.askDestroySlot()
	case tabGroups:
		return m.askDeleteGroup()
	default:
		m.status, m.stErr = "delete is available on the Persistent, Ephemeral, and Groups views", false
		return m, nil
	}
}

// askDeleteGroup confirms deleting the selected runner group (refuses Default).
func (m Model) askDeleteGroup() (tea.Model, tea.Cmd) {
	g, ok := m.groups.selected()
	if !ok {
		return m, nil
	}
	if g.Group.ID == service.DefaultGroupID {
		m.status, m.stErr = "the Default group cannot be deleted", true
		return m, nil
	}
	m.modal = newConfirm(m.theme, "Delete group?",
		fmt.Sprintf("Delete %q (%s).\nRunners in it fall back to the Default group.", g.Group.Name, g.Org))
	m.onConfirm = func() tea.Cmd { return deleteGroupCmd(m.ctx, m.mgr, g.Org, g.Group.ID, g.Group.Name) }
	m.modalOpen = true
	return m, nil
}

func (m Model) askDeleteRunner() (tea.Model, tea.Cmd) {
	row, ok := m.runners.selected()
	if !ok {
		return m, nil
	}
	r := row.Runner
	if row.Local {
		m.modal = newConfirm(m.theme, "Destroy runner?",
			fmt.Sprintf("%s (%s) lives on THIS host.\nStop its service, delete its tree, and deregister it?", r.Name, row.Org))
		m.onConfirm = func() tea.Cmd { return destroyRunnerCmd(m.ctx, m.mgr, row.Org, r.Name) }
	} else {
		m.modal = newConfirm(m.theme, "Deregister runner?",
			fmt.Sprintf("%s (%s) is remote.\nDeregister it from GitHub?", r.Name, row.Org))
		m.onConfirm = func() tea.Cmd { return deleteRunnerCmd(m.ctx, m.mgr, row.Org, r.ID, r.Name) }
	}
	m.modalOpen = true
	return m, nil
}

func (m Model) askDestroySlot() (tea.Model, tea.Cmd) {
	s, ok := m.ephemeral.selected()
	if !ok {
		return m, nil
	}
	m.modal = newConfirm(m.theme, "Destroy ephemeral slot?",
		fmt.Sprintf("slot %s (%s) lives on THIS host.\nStop its lane, delete its tree, and deregister any in-flight job?", s.Slot, s.Org))
	m.onConfirm = func() tea.Cmd { return destroyEphemeralSlotCmd(m.ctx, m.mgr, s.Org, s.Slot) }
	m.modalOpen = true
	return m, nil
}

// filterableTab reports whether the active tab supports the incremental "/"
// filter. Every list view does; Settings/Drift/Lifecycle (snapshots/menus) vary.
func (m Model) filterableTab() bool {
	switch m.tab {
	case tabPersistent, tabEphemeral, tabGroups, tabHealth, tabDrift:
		return true
	default:
		return false
	}
}

// activeFiltering reports whether the active tab's filter input is focused.
func (m Model) activeFiltering() bool {
	switch m.tab {
	case tabPersistent:
		return m.runners.filtering()
	case tabEphemeral:
		return m.ephemeral.filtering()
	case tabGroups:
		return m.groups.filtering()
	case tabHealth:
		return m.health.filtering()
	case tabDrift:
		return m.drift.filtering()
	default:
		return false
	}
}

// startFilter focuses the active tab's filter input.
func (m *Model) startFilter() tea.Cmd {
	switch m.tab {
	case tabPersistent:
		return m.runners.startFilter()
	case tabEphemeral:
		return m.ephemeral.startFilter()
	case tabGroups:
		return m.groups.startFilter()
	case tabHealth:
		return m.health.startFilter()
	case tabDrift:
		return m.drift.startFilter()
	default:
		return nil
	}
}

// stopFilter blurs the active tab's filter input (clear empties the query too).
func (m *Model) stopFilter(clear bool) {
	switch m.tab {
	case tabPersistent:
		m.runners.stopFilter(clear)
	case tabEphemeral:
		m.ephemeral.stopFilter(clear)
	case tabGroups:
		m.groups.stopFilter(clear)
	case tabHealth:
		m.health.stopFilter(clear)
	case tabDrift:
		m.drift.stopFilter(clear)
	}
}

// updateActiveFilter feeds a keystroke to the active tab's filter and re-filters.
func (m Model) updateActiveFilter(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.tab {
	case tabPersistent:
		m.runners, cmd = m.runners.updateFilter(msg)
	case tabEphemeral:
		m.ephemeral, cmd = m.ephemeral.updateFilter(msg)
	case tabGroups:
		m.groups, cmd = m.groups.updateFilter(msg)
	case tabHealth:
		m.health, cmd = m.health.updateFilter(msg)
	case tabDrift:
		m.drift, cmd = m.drift.updateFilter(msg)
	}
	return m, cmd
}

// selectableTab reports whether the active tab supports multi-select (space / A).
func (m Model) selectableTab() bool {
	return m.tab == tabPersistent || m.tab == tabEphemeral
}

// toggleSelect / selectAllVisible / clearSelections / selectionCount drive the
// multi-select substrate on the two list tabs that support it.
func (m *Model) toggleSelect() {
	switch m.tab {
	case tabPersistent:
		m.runners.toggleSelect()
	case tabEphemeral:
		m.ephemeral.toggleSelect()
	}
}

func (m *Model) selectAllVisible() {
	switch m.tab {
	case tabPersistent:
		m.runners.selectAllVisible()
	case tabEphemeral:
		m.ephemeral.selectAllVisible()
	}
}

func (m *Model) clearSelections() {
	m.runners.clearSelection()
	m.ephemeral.clearSelection()
}

func (m Model) selectionCount() int {
	switch m.tab {
	case tabPersistent:
		return m.runners.sel.count()
	case tabEphemeral:
		return m.ephemeral.sel.count()
	}
	return 0
}

// updateForm drives the create wizard. On completion it kicks off the create
// command; on abort it just closes.
func (m Model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
		m.help.SetWidth(ws.Width)
		m.layout()
	}
	fm, cmd := m.form.form.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		m.form.form = f
	}
	switch m.form.form.State {
	case huh.StateCompleted:
		spec := m.form.spec()
		ephemeral := m.form.ephemeral
		m.formOpen, m.form = false, nil
		var ch chan core.ProgressEvent
		var resCh chan createResult
		if ephemeral {
			ch, resCh = startCreateEphemeral(m.ctx, m.mgr, spec)
			m.createNoun = "ephemeral slot"
		} else {
			ch, resCh = startCreate(m.ctx, m.mgr, spec)
			m.createNoun = "runner"
		}
		m.createCh, m.createResCh = ch, resCh
		m.creating = true
		m.createDone, m.createTotal = 0, spec.Count
		m.createOrg, m.createMsg = spec.Org, "provisioning…"
		return m, tea.Batch(m.spin.Tick, waitCreateCmd(ch, resCh, spec.Org, m.createNoun))
	case huh.StateAborted:
		m.formOpen, m.form = false, nil
		m.status, m.stErr = "create cancelled", false
		return m, nil
	}
	return m, cmd
}

// updateSettingsForm drives the Settings edit form. On completion it persists the
// new capacity policy (and reloads the snapshot); on abort it just closes.
func (m Model) updateSettingsForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
		m.help.SetWidth(ws.Width)
		m.layout()
	}
	fm, cmd := m.setForm.form.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		m.setForm.form = f
	}
	switch m.setForm.form.State {
	case huh.StateCompleted:
		s := m.setForm.settings()
		m.setForm = nil
		m.loading = true
		m.status = "saving…"
		return m, saveSettingsCmd(m.mgr, s)
	case huh.StateAborted:
		m.setForm = nil
		m.status, m.stErr = "edit cancelled", false
		return m, nil
	}
	return m, cmd
}

// updateRunnerEditForm drives the persistent-runner group-edit form.
func (m Model) updateRunnerEditForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
		m.help.SetWidth(ws.Width)
		m.layout()
	}
	fm, cmd := m.runnerEdit.form.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		m.runnerEdit.form = f
	}
	switch m.runnerEdit.form.State {
	case huh.StateCompleted:
		rf := m.runnerEdit
		m.runnerEdit = nil
		m.loading = true
		m.status = "moving runner…"
		return m, moveRunnerCmd(m.ctx, m.mgr, rf.org, rf.runnerID, rf.group, rf.name)
	case huh.StateAborted:
		m.runnerEdit = nil
		m.status, m.stErr = "edit cancelled", false
		return m, nil
	}
	return m, cmd
}

// updateUpgradeForm drives the agent-upgrade options form. On completion it runs
// the dry-run preview with the chosen --to-version / --force threaded in (same scope
// startUpgrade would have used); on abort it just closes.
func (m Model) updateUpgradeForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
		m.help.SetWidth(ws.Width)
		m.layout()
	}
	fm, cmd := m.upgradeForm.form.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		m.upgradeForm.form = f
	}
	switch m.upgradeForm.form.State {
	case huh.StateCompleted:
		uf := m.upgradeForm
		m.upgradeForm = nil
		m.loading = true
		m.status = "planning upgrade…"
		return m, planUpgradeCmd(m.ctx, m.mgr, m.actionOrg(), m.selectionOnly(), uf.toVersion(), uf.force, m.theme)
	case huh.StateAborted:
		m.upgradeForm = nil
		m.status, m.stErr = "upgrade cancelled", false
		return m, nil
	}
	return m, cmd
}

// updateUninstallForm drives the uninstall Step-1 form. On completion it runs the
// dry-run blast-radius preview with the chosen scope + toggles (which then escalates
// the destructive gates); on abort it just closes.
func (m Model) updateUninstallForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
		m.help.SetWidth(ws.Width)
		m.layout()
	}
	fm, cmd := m.uninstallForm.form.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		m.uninstallForm.form = f
	}
	switch m.uninstallForm.form.State {
	case huh.StateCompleted:
		opts := m.uninstallForm.opts()
		m.uninstallForm = nil
		m.loading = true
		m.status = "computing blast radius…"
		return m, uninstallPreviewCmd(m.ctx, m.mgr, m.theme, opts)
	case huh.StateAborted:
		m.uninstallForm = nil
		m.status, m.stErr = "uninstall cancelled", false
		return m, nil
	}
	return m, cmd
}

// updateOnboardForm drives the lifecycle onboard wizard. On completion it upserts the
// org into the in-memory config, then persists + reloads + validates it off the loop;
// on abort it just closes.
func (m Model) updateOnboardForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
		m.help.SetWidth(ws.Width)
		m.layout()
	}
	fm, cmd := m.onboardForm.form.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		m.onboardForm.form = f
	}
	switch m.onboardForm.form.State {
	case huh.StateCompleted:
		oc := m.onboardForm.fields.ToOrgConfig()
		m.onboardForm = nil
		m.mgr.UpsertOrg(oc)
		m.loading = true
		m.status = "saving org " + oc.Name + "…"
		return m, onboardSaveCmd(m.ctx, m.mgr, oc.Name)
	case huh.StateAborted:
		m.onboardForm = nil
		m.status, m.stErr = "onboarding cancelled", false
		return m, nil
	}
	return m, cmd
}

// updateRetentionForm drives the Health retention edit form.
func (m Model) updateRetentionForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
		m.help.SetWidth(ws.Width)
		m.layout()
	}
	fm, cmd := m.retForm.form.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		m.retForm.form = f
	}
	switch m.retForm.form.State {
	case huh.StateCompleted:
		org, days := m.retForm.value()
		m.retForm = nil
		m.loading = true
		m.status = "saving retention…"
		return m, saveRetentionCmd(m.ctx, m.mgr, org, days)
	case huh.StateAborted:
		m.retForm = nil
		m.status, m.stErr = "retention edit cancelled", false
		return m, nil
	}
	return m, cmd
}

func (m *Model) setLoadStatus(n int, noun string, err error) {
	if err != nil {
		m.status, m.stErr = "partial: "+err.Error(), true
		return
	}
	scope := "all orgs"
	if m.orgFilter != "" {
		scope = m.orgFilter
	}
	plural := ""
	if n != 1 {
		plural = "s"
	}
	m.status, m.stErr = fmt.Sprintf("%d %s%s · %s", n, noun, plural, scope), false
}

// setFleetStatus summarizes the loaded fleet snapshot for the status bar.
func (m *Model) setFleetStatus() {
	if err := firstPartial(m.snap.PartialErrs); err != nil {
		m.status, m.stErr = "partial: "+err.Error(), true
		return
	}
	scope := "all orgs"
	if m.orgFilter != "" {
		scope = m.orgFilter
	}
	m.status, m.stErr = fmt.Sprintf("%d runners · %d slots · %s",
		len(m.snap.Runners), len(m.snap.Slots), scope), false
}

// sinceLoad is the age of the current fleet snapshot.
func (m Model) sinceLoad() time.Duration { return time.Since(m.loadedAt) }

// layout sizes the sub-views to the available body height, reserving room on the
// fleet tabs for the banner above the table and the one-line detail below it.
func (m *Model) layout() {
	bodyH := m.height - chromeHeight(m.help.ShowAll)
	if bodyH < 3 {
		bodyH = 3
	}
	listH := bodyH - m.fleetHeaderLines() - 1 // -1 for the one-line detail
	if listH < 3 {
		listH = 3
	}
	m.runners.setSize(m.width, listH)
	m.ephemeral.setSize(m.width, listH)
	m.groups.setSize(m.width, bodyH-1) // detail line
	m.health.setSize(m.width, bodyH)
	m.settings.setSize(m.width, bodyH)
	m.info.setSize(m.width, bodyH)
	// The Drift table shares the body with a class strip + a couple of context rows.
	driftH := bodyH - 4
	if driftH < 3 {
		driftH = 3
	}
	m.drift.setSize(m.width, driftH)
}

// chromeHeight is the number of non-body lines (header + tabs + status + help),
// with a small safety margin so the footer never scrolls off.
func chromeHeight(fullHelp bool) int {
	h := 7 // header(1) + gap(1) + tabbar(2) + status(1) + short help(1) + margin(1)
	if fullHelp {
		h += 3 // full help spans extra rows
	}
	return h
}

// firstPartial returns any one error from a per-org error map.
func firstPartial(errs map[string]error) error {
	for org, e := range errs {
		if e != nil {
			return fmt.Errorf("%s: %w", org, e)
		}
	}
	return nil
}
