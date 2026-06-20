package tui

import (
	"charm.land/bubbles/v2/table"
	lipgloss "charm.land/lipgloss/v2"
)

// Palette - 256-color indices chosen to read well on both dark and light
// terminals. A single place to retune the whole UI. (lipgloss.Color returns an
// interface, so these are vars, not consts.)
var (
	colPrimary = lipgloss.Color("212") // pink - brand / active
	colAccent  = lipgloss.Color("99")  // purple - secondary accent
	colDim     = lipgloss.Color("240") // borders / inactive chrome
	colMuted   = lipgloss.Color("245") // help / secondary text
	colText    = lipgloss.Color("252") // primary text
	colOK      = lipgloss.Color("42")  // green - online / success
	colWarn    = lipgloss.Color("214") // amber - busy / warning
	colErr     = lipgloss.Color("203") // red - offline / error
	colInfo    = lipgloss.Color("75")  // blue - info / local
)

// Theme holds every lipgloss style the TUI uses.
type Theme struct {
	Title    lipgloss.Style
	Crumb    lipgloss.Style
	Pill     lipgloss.Style
	TabOn    lipgloss.Style
	TabOff   lipgloss.Style
	TabRule  lipgloss.Style
	Panel    lipgloss.Style
	PanelTtl lipgloss.Style
	Help     lipgloss.Style

	StatusInfo lipgloss.Style
	StatusOK   lipgloss.Style
	StatusErr  lipgloss.Style
	Spin       lipgloss.Style

	Modal  lipgloss.Style
	ModalT lipgloss.Style
	BtnOn  lipgloss.Style
	BtnOff lipgloss.Style

	Online  lipgloss.Style
	Offline lipgloss.Style
	Busy    lipgloss.Style
	OnHost  lipgloss.Style
	Remote  lipgloss.Style
	Faint   lipgloss.Style

	Table table.Styles
}

// NewTheme returns the default theme.
func NewTheme() Theme {
	tbl := table.DefaultStyles()
	tbl.Header = tbl.Header.
		Bold(true).
		Foreground(colMuted).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(colDim)
	tbl.Selected = tbl.Selected.
		Bold(true).
		Foreground(colPrimary).
		Background(lipgloss.Color("236"))
	// Cell carries NO foreground on purpose: a per-cell foreground makes bubbles
	// emit an ANSI reset at every cell boundary (table.go renderRow), and that
	// reset truncates the row-level Selected background to just the first column.
	// Leaving cells uncolored lets Selected's background span the full row width.
	tbl.Cell = tbl.Cell.UnsetForeground()

	return Theme{
		Title: lipgloss.NewStyle().Bold(true).Foreground(colPrimary),
		Crumb: lipgloss.NewStyle().Foreground(colMuted),
		Pill:  lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(colAccent).Padding(0, 1).Bold(true),
		// Both tab states carry the same bottom-border row so every tab is the same
		// height and their labels share a baseline (without it the active tab is one
		// row taller and JoinHorizontal drops the inactive labels onto the rule row).
		// The active tab gets a bright underline; inactive tabs a dim one - together a
		// continuous tab rule with the selection highlighted.
		TabOn:    lipgloss.NewStyle().Bold(true).Foreground(colPrimary).Padding(0, 2).Border(lipgloss.RoundedBorder(), false, false, true, false).BorderForeground(colPrimary),
		TabOff:   lipgloss.NewStyle().Foreground(colMuted).Padding(0, 2).Border(lipgloss.RoundedBorder(), false, false, true, false).BorderForeground(colDim),
		TabRule:  lipgloss.NewStyle().Foreground(colDim),
		Panel:    lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colDim).Padding(0, 1),
		PanelTtl: lipgloss.NewStyle().Bold(true).Foreground(colAccent),
		Help:     lipgloss.NewStyle().Foreground(colMuted),

		StatusInfo: lipgloss.NewStyle().Foreground(colInfo),
		StatusOK:   lipgloss.NewStyle().Bold(true).Foreground(colOK),
		StatusErr:  lipgloss.NewStyle().Bold(true).Foreground(colErr),
		Spin:       lipgloss.NewStyle().Foreground(colPrimary),

		Modal:  lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colPrimary).Padding(1, 3),
		ModalT: lipgloss.NewStyle().Bold(true).Foreground(colPrimary),
		BtnOn:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(colAccent).Padding(0, 2),
		BtnOff: lipgloss.NewStyle().Foreground(colMuted).Padding(0, 2),

		Online:  lipgloss.NewStyle().Foreground(colOK),
		Offline: lipgloss.NewStyle().Foreground(colErr),
		Busy:    lipgloss.NewStyle().Foreground(colWarn),
		OnHost:  lipgloss.NewStyle().Foreground(colOK).Bold(true),
		Remote:  lipgloss.NewStyle().Foreground(colMuted),
		Faint:   lipgloss.NewStyle().Foreground(colDim),

		Table: tbl,
	}
}

// fillWidth grows the LAST column so the table's rows - and the full-width
// selection highlight + header rule - span the terminal width w. bubbles renders
// every cell with one column of horizontal padding per side, so a row is
// sum(col.Width)+2*len(cols) wide; the leftover is handed to the trailing column
// (slack sits at the right edge, the natural full-width look).
func fillWidth(cols []table.Column, w int) []table.Column {
	if len(cols) == 0 || w <= 0 {
		return cols
	}
	out := make([]table.Column, len(cols))
	copy(out, cols)
	used := 2 * len(cols)
	for _, c := range cols {
		used += c.Width
	}
	if extra := w - used; extra > 0 {
		out[len(out)-1].Width += extra
	}
	return out
}
