package tui

import "charm.land/bubbles/v2/key"

// keyMap is the global key set. It implements help.KeyMap so the help bubble can
// render both the short (one-line) and full (multi-column) views.
type keyMap struct {
	Up        key.Binding
	Down      key.Binding
	Tab       key.Binding
	ShiftTab  key.Binding
	Org       key.Binding
	Refresh   key.Binding
	Auto      key.Binding
	New       key.Binding
	Delete    key.Binding
	Edit      key.Binding
	Filter    key.Binding
	Enter     key.Binding
	Esc       key.Binding
	Info      key.Binding
	Select    key.Binding
	SelectAll key.Binding
	Dismiss   key.Binding
	Help      key.Binding
	Quit      key.Binding

	// Control-plane / drift ops (Phase 2-3). Registered here so they appear in the
	// help automatically; their availability is gated per-tab in the key handler.
	Refresh2  key.Binding // R - refresh units
	Upgrade   key.Binding // u - upgrade agent
	Rollback  key.Binding // b - rollback agent
	Prune     key.Binding // p - prune dep-cache
	Provision key.Binding // P - provision host
	Recreate  key.Binding // c - recreate runner/slot
	Fix       key.Binding // f - reconcile fix
	Reap      key.Binding // g - reap ephemeral ghosts
}

func newKeyMap() keyMap {
	return keyMap{
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Tab:       key.NewBinding(key.WithKeys("tab", "l", "right"), key.WithHelp("tab", "next view")),
		ShiftTab:  key.NewBinding(key.WithKeys("shift+tab", "h", "left"), key.WithHelp("shift+tab", "prev view")),
		Org:       key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "filter orgs")),
		Refresh:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Auto:      key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "auto-refresh")),
		New:       key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
		Delete:    key.NewBinding(key.WithKeys("d", "delete"), key.WithHelp("d", "delete/destroy")),
		Edit:      key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		Filter:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Enter:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open/confirm")),
		Esc:       key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Info:      key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "information")),
		Select:    key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "select")),
		SelectAll: key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "select/clear all")),
		Dismiss:   key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "dismiss banner")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),

		Refresh2:  key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "refresh units")),
		Upgrade:   key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "upgrade agent")),
		Rollback:  key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "rollback")),
		Prune:     key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "prune cache")),
		Provision: key.NewBinding(key.WithKeys("P"), key.WithHelp("P", "provision")),
		Recreate:  key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "recreate")),
		Fix:       key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "fix drift")),
		Reap:      key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "reap ghosts")),
	}
}

// ShortHelp is the one-line help (most-used keys).
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Tab, k.Enter, k.Refresh, k.New, k.Delete, k.Help, k.Quit}
}

// FullHelp is the expanded, columnar help.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Filter, k.Info, k.Enter},
		{k.Tab, k.ShiftTab, k.Org, k.Auto},
		{k.New, k.Delete, k.Edit, k.Refresh},
		{k.Select, k.SelectAll, k.Dismiss, k.Esc},
		{k.Upgrade, k.Rollback, k.Refresh2, k.Recreate},
		{k.Prune, k.Provision, k.Fix, k.Reap},
		{k.Help, k.Quit},
	}
}
