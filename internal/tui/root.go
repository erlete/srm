// Package tui implements the Bubble Tea (v2) terminal UI: a tabbed, multi-org
// cockpit over the service layer. Five views - Persistent runners, Ephemeral
// slots, Groups, Health, and Settings - share a header (org filter), a spinner-
// driven status bar, an integrated help bubble, and a confirmation modal for
// destructive actions. The Persistent and Ephemeral panels are deliberately
// SEPARATE: the two runner natures must never be mistaken, so each has its own
// columns, create/destroy flows, and addressing (name vs slot id). Every GitHub
// call runs inside a tea.Cmd; Update never blocks.
package tui

import (
	"context"
	"fmt"

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
	tabPersistent tab = iota
	tabEphemeral
	tabGroups
	tabHealth
	tabSettings
)

var tabNames = []string{"Persistent", "Ephemeral", "Groups", "Health", "Settings"}

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
	orgFilter string // "" = all configured orgs

	runners   runnersView
	ephemeral ephemeralView
	groups    groupsView
	health    healthView
	settings  settingsView

	loading bool
	status  string
	stErr   bool

	modalOpen bool
	modal     confirmModal
	onConfirm func() tea.Cmd

	formOpen bool
	form     *createForm

	setForm *settingsForm // Settings edit form (nil = closed)

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
		ctx:       ctx,
		mgr:       mgr,
		theme:     t,
		keys:      newKeyMap(),
		help:      h,
		spin:      sp,
		prog:      progress.New(progress.WithWidth(48)),
		runners:   newRunnersView(t),
		ephemeral: newEphemeralView(t),
		groups:    newGroupsView(t),
		health:    newHealthView(t),
		settings:  newSettingsView(t),
		loading:   true,
	}
}

// Init starts the spinner and loads the first view.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, m.loadCurrent())
}

// loadCurrent returns the load command for the active tab + org filter.
func (m Model) loadCurrent() tea.Cmd {
	switch m.tab {
	case tabEphemeral:
		return loadEphemeralCmd(m.ctx, m.mgr, m.orgFilter)
	case tabGroups:
		return loadGroupsCmd(m.ctx, m.mgr, m.orgFilter)
	case tabHealth:
		return loadHealthCmd(m.ctx, m.mgr, m.orgFilter)
	case tabSettings:
		return loadSettingsCmd(m.mgr)
	default:
		return loadRunnersCmd(m.ctx, m.mgr, m.orgFilter)
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

	case runnersMsg:
		m.loading = false
		m.runners.setRows(msg.rows)
		m.setLoadStatus(len(msg.rows), "runner", msg.err)
		return m, nil

	case ephemeralMsg:
		m.loading = false
		m.ephemeral.setRows(msg.rows)
		m.setLoadStatus(len(msg.rows), "slot", msg.err)
		return m, nil

	case settingsMsg:
		m.loading = false
		m.settings.setSnapshot(msg.snap)
		m.status, m.stErr = "capacity policy", false
		return m, nil

	case groupsMsg:
		m.loading = false
		m.groups.setRows(msg.rows)
		m.setLoadStatus(len(msg.rows), "group", msg.err)
		return m, nil

	case healthMsg:
		m.loading = false
		m.health.setReports(msg.reports)
		m.setLoadStatus(len(msg.reports), "org", msg.err)
		return m, nil

	case actionMsg:
		if msg.err != nil {
			m.status, m.stErr = msg.err.Error(), true
			return m, nil
		}
		m.status, m.stErr = msg.summary, false
		m.loading = true
		return m, m.loadCurrent()

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
		m.loading = true
		return m, m.loadCurrent()
	}

	return m, nil
}

func (m Model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// While a create is streaming, only quit is honored.
	if m.creating {
		if key.Matches(msg, m.keys.Quit) {
			return m, tea.Quit
		}
		return m, nil
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

	// Modal captures all keys while open.
	if m.modalOpen {
		switch {
		case key.Matches(msg, m.keys.Quit), key.Matches(msg, m.keys.Esc):
			m.modalOpen = false
		case msg.String() == "left", msg.String() == "right", key.Matches(msg, m.keys.Tab), key.Matches(msg, m.keys.ShiftTab):
			m.modal.toggle()
		case msg.String() == "y":
			return m.runConfirmed()
		case msg.String() == "n":
			m.modalOpen = false
		case key.Matches(msg, m.keys.Enter):
			if m.modal.yes {
				return m.runConfirmed()
			}
			m.modalOpen = false
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
		m.loading = true
		return m, m.loadCurrent()
	case key.Matches(msg, m.keys.ShiftTab):
		m.tab = (m.tab + tab(len(tabNames)) - 1) % tab(len(tabNames))
		m.loading = true
		return m, m.loadCurrent()
	case key.Matches(msg, m.keys.Org):
		m.cycleOrg()
		m.loading = true
		return m, m.loadCurrent()
	case key.Matches(msg, m.keys.Refresh):
		m.loading = true
		m.status = ""
		return m, m.loadCurrent()
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
		default:
			m.status, m.stErr = "create is available on the Persistent and Ephemeral views", false
			return m, nil
		}
	case key.Matches(msg, m.keys.Delete):
		return m.askDelete()
	case key.Matches(msg, m.keys.Edit):
		if m.tab != tabSettings {
			m.status, m.stErr = "edit is available on the Settings view", false
			return m, nil
		}
		m.setForm = newSettingsForm(m.mgr.ResourceSettings())
		return m, m.setForm.form.Init()
	}

	// Delegate movement to the active view's table.
	var cmd tea.Cmd
	switch m.tab {
	case tabEphemeral:
		m.ephemeral, cmd = m.ephemeral.update(msg)
	case tabGroups:
		m.groups, cmd = m.groups.update(msg)
	case tabHealth, tabSettings:
		// no table interaction
	default:
		m.runners, cmd = m.runners.update(msg)
	}
	return m, cmd
}

// askDelete opens the confirm modal for the highlighted item. On the Persistent
// view a local runner is destroyed (host teardown + deregister) and a remote one
// is only deregistered; on the Ephemeral view a slot lane is torn down by slot id.
func (m Model) askDelete() (tea.Model, tea.Cmd) {
	switch m.tab {
	case tabPersistent:
		return m.askDeleteRunner()
	case tabEphemeral:
		return m.askDestroySlot()
	default:
		m.status, m.stErr = "delete is available on the Persistent and Ephemeral views", false
		return m, nil
	}
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
// filter. Every list view does; Settings (a single snapshot) does not.
func (m Model) filterableTab() bool {
	switch m.tab {
	case tabPersistent, tabEphemeral, tabGroups, tabHealth:
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
	}
	return m, cmd
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

func (m Model) runConfirmed() (tea.Model, tea.Cmd) {
	m.modalOpen = false
	if m.onConfirm == nil {
		return m, nil
	}
	cmd := m.onConfirm()
	m.onConfirm = nil
	m.loading = true
	m.status = "working…"
	return m, cmd
}

// cycleOrg advances the org filter through [All, org1, org2, …].
func (m *Model) cycleOrg() {
	ring := append([]string{""}, m.mgr.OrgNames()...)
	idx := 0
	for i, o := range ring {
		if o == m.orgFilter {
			idx = i
			break
		}
	}
	m.orgFilter = ring[(idx+1)%len(ring)]
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

// layout sizes the sub-views to the available body height.
func (m *Model) layout() {
	bodyH := m.height - chromeHeight(m.help.ShowAll)
	if bodyH < 3 {
		bodyH = 3
	}
	m.runners.setSize(m.width, bodyH-1)   // -1 for the detail line
	m.ephemeral.setSize(m.width, bodyH-1) // detail line too
	m.groups.setSize(m.width, bodyH-1)    // detail line too
	m.health.setSize(m.width, bodyH)
	m.settings.setSize(m.width, bodyH)
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
