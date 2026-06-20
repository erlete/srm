package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// filterState is the incremental text filter shared by every filterable view
// (Persistent, Ephemeral, Groups, Health). It owns the textinput and the focus
// flag; each view supplies its own haystack by calling query() inside its
// applyFilter. Centralizing it means "/" behaves identically on every tab - focus
// to type, esc clears, enter keeps - and the views differ only in WHAT they match.
type filterState struct {
	input  textinput.Model
	active bool // the input is focused (capturing keystrokes)
}

func newFilterState(placeholder string) filterState {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Prompt = ""
	return filterState{input: ti}
}

// start focuses the input so keystrokes flow into the query.
func (f *filterState) start() tea.Cmd { f.active = true; return f.input.Focus() }

// stop blurs the input; clear also empties the query (esc) versus keeping it (enter).
func (f *filterState) stop(clear bool) {
	f.active = false
	f.input.Blur()
	if clear {
		f.input.SetValue("")
	}
}

// filtering reports whether the input is currently focused.
func (f filterState) filtering() bool { return f.active }

// query is the normalized (lower-cased, trimmed) filter text; "" means no filter.
func (f filterState) query() string { return strings.ToLower(strings.TrimSpace(f.input.Value())) }

// shown reports whether the filter line should be visible: focused, or holding a
// non-empty query the user blurred but kept.
func (f filterState) shown() bool { return f.active || strings.TrimSpace(f.input.Value()) != "" }

// setWidth fits the input to the available width.
func (f *filterState) setWidth(w int) { f.input.SetWidth(w) }

// update feeds a message to the input and returns the new state + cmd. Callers
// re-run their applyFilter afterwards so the visible rows track the query.
func (f filterState) update(msg tea.Msg) (filterState, tea.Cmd) {
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return f, cmd
}

// line renders the "/ <query>" filter line shown beneath (or above) a view.
func (f filterState) line(t Theme) string {
	return t.StatusInfo.Render("/ ") + f.input.View()
}
