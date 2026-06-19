package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

// runnersView is the runners inventory: a cross-org table (ORG/MACHINE columns),
// an incremental text filter, and a colored detail line for the selection.
type runnersView struct {
	tbl    table.Model
	cols   []table.Column          // base column widths (re-fitted to the terminal on resize)
	all    []service.RunnerWithOrg // full set
	rows   []service.RunnerWithOrg // filtered (mirrors the table rows)
	theme  Theme
	width  int
	filter textinput.Model
	active bool // filter input is focused
}

func newRunnersView(t Theme) runnersView {
	cols := []table.Column{
		{Title: "ORG", Width: 15},
		{Title: "ID", Width: 6},
		{Title: "NAME", Width: 26},
		{Title: "STATUS", Width: 8},
		{Title: "JOB", Width: 5},
		{Title: "MACHINE", Width: 8},
		{Title: "LABELS", Width: 30},
	}
	tbl := table.New(table.WithColumns(cols), table.WithFocused(true))
	tbl.SetStyles(t.Table)

	ti := textinput.New()
	ti.Placeholder = "type to filter by org / name / status / label…"
	ti.Prompt = ""
	return runnersView{tbl: tbl, cols: cols, theme: t, filter: ti}
}

func (v *runnersView) setSize(w, h int) {
	v.width = w
	v.tbl.SetWidth(w)
	v.tbl.SetColumns(fillWidth(v.cols, w))
	v.filter.SetWidth(w - 4)
	if h > 3 {
		v.tbl.SetHeight(h)
	}
}

func (v *runnersView) setRows(rows []service.RunnerWithOrg) {
	v.all = rows
	v.applyFilter()
}

// applyFilter recomputes the visible rows from the filter query.
func (v *runnersView) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(v.filter.Value()))
	v.rows = v.rows[:0]
	for _, row := range v.all {
		if q == "" || strings.Contains(v.haystack(row), q) {
			v.rows = append(v.rows, row)
		}
	}
	tr := make([]table.Row, 0, len(v.rows))
	for _, row := range v.rows {
		r := row.Runner
		job := "idle"
		if r.Busy {
			job = "busy"
		}
		machine := "remote"
		if row.Local {
			machine = "this"
		}
		tr = append(tr, table.Row{
			row.Org, strconv.FormatInt(r.ID, 10), r.Name,
			r.Status, job, machine, labelString(r.Labels),
		})
	}
	v.tbl.SetRows(tr)
}

func (v runnersView) haystack(row service.RunnerWithOrg) string {
	return strings.ToLower(strings.Join([]string{
		row.Org, row.Runner.Name, row.Runner.Status, labelString(row.Runner.Labels),
	}, " "))
}

// startFilter focuses the filter input. stopFilter blurs it, optionally clearing.
func (v *runnersView) startFilter() tea.Cmd { v.active = true; return v.filter.Focus() }
func (v *runnersView) stopFilter(clear bool) {
	v.active = false
	v.filter.Blur()
	if clear {
		v.filter.SetValue("")
		v.applyFilter()
	}
}

func (v runnersView) filtering() bool { return v.active }

func (v runnersView) updateFilter(msg tea.Msg) (runnersView, tea.Cmd) {
	var cmd tea.Cmd
	v.filter, cmd = v.filter.Update(msg)
	v.applyFilter()
	return v, cmd
}

func (v runnersView) update(msg tea.Msg) (runnersView, tea.Cmd) {
	var cmd tea.Cmd
	v.tbl, cmd = v.tbl.Update(msg)
	return v, cmd
}

func (v runnersView) selected() (service.RunnerWithOrg, bool) {
	i := v.tbl.Cursor()
	if i < 0 || i >= len(v.rows) {
		return service.RunnerWithOrg{}, false
	}
	return v.rows[i], true
}

func (v runnersView) view() string {
	last := v.detail()
	if v.active || strings.TrimSpace(v.filter.Value()) != "" {
		last = v.theme.StatusInfo.Render("/ ") + v.filter.View()
	}
	return lipgloss.JoinVertical(lipgloss.Left, v.tbl.View(), last)
}

func (v runnersView) detail() string {
	row, ok := v.selected()
	if !ok {
		return v.theme.Help.Render("no runners match")
	}
	r := row.Runner
	parts := []string{
		v.theme.Title.Render(r.Name),
		v.theme.Crumb.Render(row.Org),
		v.statusBadge(r),
		v.machineBadge(row.Local),
	}
	if ls := labelString(r.Labels); ls != "" {
		parts = append(parts, v.theme.Faint.Render(ls))
	}
	return strings.Join(parts, v.theme.Faint.Render(" · "))
}

func (v runnersView) statusBadge(r core.Runner) string {
	switch {
	case r.Busy:
		return v.theme.Busy.Render("● busy")
	case r.Online():
		return v.theme.Online.Render("● online")
	default:
		return v.theme.Offline.Render("● offline")
	}
}

func (v runnersView) machineBadge(local bool) string {
	if local {
		return v.theme.OnHost.Render("⌂ this host")
	}
	return v.theme.Remote.Render("remote")
}

// counts returns colored online/busy/offline tallies for the header.
func (v runnersView) counts() string {
	var online, offline, busy int
	for _, row := range v.rows {
		switch {
		case row.Runner.Busy:
			busy++
		case row.Runner.Online():
			online++
		default:
			offline++
		}
	}
	return fmt.Sprintf("%s  %s  %s",
		v.theme.Online.Render(fmt.Sprintf("●%d online", online)),
		v.theme.Busy.Render(fmt.Sprintf("●%d busy", busy)),
		v.theme.Offline.Render(fmt.Sprintf("●%d offline", offline)),
	)
}

func labelString(labels []core.Label) string {
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return strings.Join(names, ",")
}
