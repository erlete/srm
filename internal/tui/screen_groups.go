package tui

import (
	"strconv"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/service"
)

// groupsView is a cross-org table of runner groups.
type groupsView struct {
	tbl   table.Model
	cols  []table.Column // base column widths (re-fitted to the terminal on resize)
	rows  []service.GroupWithOrg
	theme Theme
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
	tbl := table.New(table.WithColumns(cols), table.WithFocused(true))
	tbl.SetStyles(t.Table)
	return groupsView{tbl: tbl, cols: cols, theme: t}
}

func (v *groupsView) setSize(w, h int) {
	v.tbl.SetWidth(w)
	v.tbl.SetColumns(fillWidth(v.cols, w))
	if h > 3 {
		v.tbl.SetHeight(h)
	}
}

func (v *groupsView) setRows(rows []service.GroupWithOrg) {
	v.rows = rows
	tr := make([]table.Row, 0, len(rows))
	for _, row := range rows {
		g := row.Group
		tr = append(tr, table.Row{
			row.Org, strconv.FormatInt(g.ID, 10), g.Name,
			g.Visibility, yesNo(g.Default), yesNo(g.AllowsPublic),
		})
	}
	v.tbl.SetRows(tr)
}

func (v groupsView) update(msg tea.Msg) (groupsView, tea.Cmd) {
	var cmd tea.Cmd
	v.tbl, cmd = v.tbl.Update(msg)
	return v, cmd
}

func (v groupsView) view() string { return v.tbl.View() }

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "—"
}
