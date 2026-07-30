package tui

import (
	lipgloss "charm.land/lipgloss/v2"
)

// lifecycleAction identifies a Lifecycle-tab card's operation.
type lifecycleAction int

const (
	lifeBackup lifecycleAction = iota
	lifeProvision
	lifeManifest
	lifeUninstall
	lifeOnboard
	lifeEditOrg
	lifeRestore
)

// lifecycleCard is one operation in the Lifecycle menu.
type lifecycleCard struct {
	action  lifecycleAction
	title   string
	desc    string
	hostOp  bool // needs root on the host
	enabled bool // false = informational only (CLI-driven for now)
}

// lifecycleView is the Lifecycle tab: a cursor-driven card menu of host setup and
// teardown operations. enter runs the focused card; up/down move. It is deliberately
// not a data table - these are operations, not entities.
type lifecycleView struct {
	theme  Theme
	cursor int
	cards  []lifecycleCard
}

func newLifecycleView(t Theme) lifecycleView {
	return lifecycleView{
		theme: t,
		cards: []lifecycleCard{
			{lifeBackup, "Back up config", "Snapshot config.yaml + secrets.age + per-org keys to a tarball.", false, true},
			{lifeProvision, "Provision host deps", "Install the configured host dependency manifest (apt/scripts/tool-cache).", true, true},
			{lifeManifest, "Edit host manifest", "Edit the host dependency manifest (apt packages, setup scripts incl. inline, tool-cache seeds, cache paths). Provision applies it.", false, true},
			{lifeUninstall, "Uninstall (guarded)", "Tear down srm-created units, users, and trees on this host. Dry-run blast radius, then a typed hostname confirm.", true, true},
			{lifeOnboard, "Onboard a new org", "Add an org's GitHub App creds, save + reload config, and verify auth - the same form as `srm init`.", false, true},
			{lifeEditOrg, "Edit an org", "Re-edit an org's App creds + runner defaults (prefilled; key defaults to keep). Filter to one org (o) first if several are configured.", false, true},
			{lifeRestore, "Restore config", "Pick a config backup tarball, confirm, and restore it - then the Manager reloads in place.", false, true},
		},
	}
}

func (v *lifecycleView) move(delta int) {
	v.cursor += delta
	if v.cursor < 0 {
		v.cursor = 0
	}
	if v.cursor >= len(v.cards) {
		v.cursor = len(v.cards) - 1
	}
}

func (v lifecycleView) selected() lifecycleCard { return v.cards[v.cursor] }

// lifecyclePanel renders the Lifecycle operations menu as the RIGHT panel of the
// Settings split: a cursor-driven card list (↑/↓ move, enter runs the focused card).
func (m Model) lifecyclePanel(w int) string {
	t := m.theme
	v := m.lifecycle
	rows := []string{t.PanelTtl.Render("Lifecycle"), ""}
	for i, c := range v.cards {
		marker := "  "
		title := c.title
		if i == v.cursor {
			marker = t.Title.Render("> ")
			title = t.Title.Render(title)
		} else {
			title = t.Crumb.Render(title)
		}
		tags := ""
		if c.hostOp && !m.hostCapable {
			tags = t.Offline.Render("  (needs root)")
		} else if !c.enabled {
			tags = t.Faint.Render("  (CLI)")
		}
		rows = append(rows, marker+title+tags)
		rows = append(rows, t.Faint.Render("    "+c.desc))
	}
	inner := w - 4
	if inner < 28 {
		inner = 28
	}
	return t.Panel.Width(inner).Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}
