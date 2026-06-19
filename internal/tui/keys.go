package tui

import "charm.land/bubbles/v2/key"

// keyMap is the global key set. It implements help.KeyMap so the help bubble can
// render both the short (one-line) and full (multi-column) views.
type keyMap struct {
	Up       key.Binding
	Down     key.Binding
	Tab      key.Binding
	ShiftTab key.Binding
	Org      key.Binding
	Refresh  key.Binding
	New      key.Binding
	Delete   key.Binding
	Edit     key.Binding
	Filter   key.Binding
	Enter    key.Binding
	Esc      key.Binding
	Help     key.Binding
	Quit     key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Tab:      key.NewBinding(key.WithKeys("tab", "l", "right"), key.WithHelp("tab", "next view")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab", "h", "left"), key.WithHelp("shift+tab", "prev view")),
		Org:      key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "cycle org")),
		Refresh:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		New:      key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
		Delete:   key.NewBinding(key.WithKeys("d", "delete"), key.WithHelp("d", "delete/destroy")),
		Edit:     key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit settings")),
		Filter:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Enter:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		Esc:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// ShortHelp is the one-line help (most-used keys).
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Tab, k.Org, k.Refresh, k.New, k.Delete, k.Help, k.Quit}
}

// FullHelp is the expanded, columnar help.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Filter},
		{k.Tab, k.ShiftTab, k.Org},
		{k.Refresh, k.New, k.Delete, k.Edit},
		{k.Enter, k.Esc, k.Help, k.Quit},
	}
}
