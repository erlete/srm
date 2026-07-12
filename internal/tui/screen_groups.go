package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/erlete/srm/internal/service"
)

// groupsView is a cross-org table of runner groups with an incremental text
// filter and a colored detail line for the selection (mirrors the runners view).
type groupsView struct {
	st    scrollTable
	cols  []table.Column         // base column widths (re-fitted to the terminal on resize)
	all   []service.GroupWithOrg // full set
	rows  []service.GroupWithOrg // filtered (mirrors the table rows)
	theme Theme
	flt   filterState
}

func newGroupsView(t Theme) groupsView {
	cols := []table.Column{
		{Title: "ORG", Width: 15},
		{Title: "ID", Width: 6},
		{Title: "NAME", Width: 24},
		{Title: "VISIBILITY", Width: 11},
		{Title: "DEFAULT", Width: 8},
		{Title: "PUBLIC", Width: 7},
	}
	return groupsView{st: newScrollTable(cols, t.Table), cols: cols, theme: t, flt: newFilterState("type to filter by org / name / visibility…")}
}

func (v *groupsView) setSize(w, h int) {
	v.st.setWidth(w)
	v.st.setColumns(fillWidth(v.cols, w))
	v.flt.setWidth(w - 4)
	if h > 3 {
		v.st.setHeight(h)
	}
}

func (v *groupsView) setRows(rows []service.GroupWithOrg) {
	v.all = rows
	v.applyFilter()
}

// applyFilter recomputes the visible group rows from the filter query.
func (v *groupsView) applyFilter() {
	q := v.flt.query()
	v.rows = v.rows[:0]
	for _, row := range v.all {
		if q == "" || strings.Contains(v.haystack(row), q) {
			v.rows = append(v.rows, row)
		}
	}
	tr := make([]table.Row, 0, len(v.rows))
	for _, row := range v.rows {
		g := row.Group
		tr = append(tr, table.Row{
			row.Org, strconv.FormatInt(g.ID, 10), g.Name,
			g.Visibility, yesNo(g.Default), yesNo(g.AllowsPublic),
		})
	}
	v.st.setRows(tr)
}

func (v groupsView) haystack(row service.GroupWithOrg) string {
	return strings.ToLower(strings.Join([]string{
		row.Org, row.Group.Name, row.Group.Visibility,
	}, " "))
}

// startFilter focuses the filter input. stopFilter blurs it, optionally clearing.
func (v *groupsView) startFilter() tea.Cmd { return v.flt.start() }
func (v *groupsView) stopFilter(clear bool) {
	v.flt.stop(clear)
	if clear {
		v.applyFilter()
	}
}

func (v groupsView) filtering() bool { return v.flt.filtering() }

func (v groupsView) updateFilter(msg tea.Msg) (groupsView, tea.Cmd) {
	var cmd tea.Cmd
	v.flt, cmd = v.flt.update(msg)
	v.applyFilter()
	return v, cmd
}

func (v groupsView) update(msg tea.Msg) (groupsView, tea.Cmd) {
	var cmd tea.Cmd
	cmd = v.st.update(msg)
	return v, cmd
}

func (v groupsView) selected() (service.GroupWithOrg, bool) {
	i := v.st.cursor()
	if i < 0 || i >= len(v.rows) {
		return service.GroupWithOrg{}, false
	}
	return v.rows[i], true
}

func (v groupsView) view() string {
	last := v.detail()
	if v.flt.shown() {
		last = v.flt.line(v.theme)
	}
	return lipgloss.JoinVertical(lipgloss.Left, v.st.view(), last)
}

func (v groupsView) detail() string {
	row, ok := v.selected()
	if !ok {
		if v.flt.shown() {
			return v.theme.Help.Render("no groups match")
		}
		return v.theme.Help.Render("no runner groups")
	}
	g := row.Group
	parts := []string{
		v.theme.Title.Render(g.Name),
		v.theme.Crumb.Render(row.Org),
		v.theme.Faint.Render("visibility " + g.Visibility),
	}
	if g.Default {
		parts = append(parts, v.theme.Online.Render("default"))
	}
	if g.AllowsPublic {
		parts = append(parts, v.theme.Busy.Render("public repos"))
	}
	return strings.Join(parts, v.theme.Faint.Render(" · "))
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "-"
}
