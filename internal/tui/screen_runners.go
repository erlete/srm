package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

// runnersView is the persistent runners inventory: a cross-org table fed from the
// fused fleet snapshot (so it shows agent VERSION, DRIFT, OS, and GROUP alongside
// the live GitHub status), an incremental filter, multi-select, and a colored
// detail line / drill-down pane for the selection. The table itself stays
// monochrome on purpose - per-cell color injects ANSI resets that clip the full-
// width selection highlight (see selection_test.go) - so badges/colors live in the
// detail line, strips, and header, never in cells.
type runnersView struct {
	st    scrollTable
	cols  []table.Column        // base column widths (re-fitted to the terminal on resize)
	all   []service.FusedRunner // full set (mirrors the snapshot)
	rows  []service.FusedRunner // filtered (mirrors the table rows)
	theme Theme
	width int
	flt   filterState
	sel   selectionSet
}

func newRunnersView(t Theme) runnersView {
	cols := []table.Column{
		{Title: "", Width: 3}, // multi-select gutter
		{Title: "ORG", Width: 14},
		{Title: "ID", Width: 5},
		{Title: "NAME", Width: 22},
		{Title: "STATUS", Width: 8},
		{Title: "JOB", Width: 6},
		{Title: "VERSION", Width: 11},
		{Title: "DRIFT", Width: 11},
		{Title: "OS", Width: 6},
		{Title: "GROUP", Width: 7},
		{Title: "LABELS", Width: 16},
	}
	return runnersView{st: newScrollTable(cols, t.Table), cols: cols, theme: t, sel: newSelectionSet(),
		flt: newFilterState("type to filter by org / name / status / version / drift / label…")}
}

func (v *runnersView) setSize(w, h int) {
	v.width = w
	v.st.setWidth(w)
	v.st.setColumns(fillWidth(v.cols, w))
	v.flt.setWidth(w - 4)
	if h > 3 {
		v.st.setHeight(h)
	}
}

func (v *runnersView) setRows(rows []service.FusedRunner) {
	v.all = rows
	v.applyFilter()
}

// applyFilter recomputes the visible rows from the filter query, preserving the
// cursor position so a re-render (e.g. a selection toggle or a host-tier merge)
// does not jump the highlight.
func (v *runnersView) applyFilter() {
	cursor := v.st.cursor()
	q := v.flt.query()
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
			job = "▸ busy" // a small play marker for an in-flight job (spaced per the icon+text rule)
		}
		tr = append(tr, table.Row{
			v.sel.gutter(runnerKey(row)), row.Org, strconv.FormatInt(r.ID, 10), r.Name,
			r.Status, job, versionCell(row), driftCell(row.DriftClass, row.HostKnown),
			osCell(r.OS), groupCell(row.GroupName, r.GroupID), labelString(r.Labels),
		})
	}
	v.st.setRows(tr)
	if cursor >= len(tr) {
		cursor = len(tr) - 1
	}
	if cursor >= 0 {
		v.st.setCursor(cursor)
	}
}

func (v runnersView) haystack(row service.FusedRunner) string {
	_, drift := driftGlyph(row.DriftClass)
	return strings.ToLower(strings.Join([]string{
		row.Org, row.Runner.Name, row.Runner.Status, row.AgentVersion, drift,
		row.Runner.OS, labelString(row.Runner.Labels),
	}, " "))
}

// startFilter focuses the filter input. stopFilter blurs it, optionally clearing.
func (v *runnersView) startFilter() tea.Cmd { return v.flt.start() }
func (v *runnersView) stopFilter(clear bool) {
	v.flt.stop(clear)
	if clear {
		v.applyFilter()
	}
}

func (v runnersView) filtering() bool { return v.flt.filtering() }

func (v runnersView) updateFilter(msg tea.Msg) (runnersView, tea.Cmd) {
	var cmd tea.Cmd
	v.flt, cmd = v.flt.update(msg)
	v.applyFilter()
	return v, cmd
}

func (v runnersView) update(msg tea.Msg) (runnersView, tea.Cmd) {
	cmd := v.st.update(msg)
	return v, cmd
}

func (v runnersView) selected() (service.FusedRunner, bool) {
	i := v.st.cursor()
	if i < 0 || i >= len(v.rows) {
		return service.FusedRunner{}, false
	}
	return v.rows[i], true
}

// focusKey moves the cursor to the row with the given key, if it is in the current
// (filtered) set. Used for the Drift-tab jump-to-runner action.
func (v *runnersView) focusKey(key string) {
	for i, row := range v.rows {
		if runnerKey(row) == key {
			v.st.setCursor(i)
			return
		}
	}
}

// toggleSelect flips the cursor row's membership in the multi-select set.
func (v *runnersView) toggleSelect() {
	if row, ok := v.selected(); ok {
		v.sel.toggle(runnerKey(row))
		v.applyFilter()
	}
}

// selectAllVisible toggles bulk selection: if every visible row is already
// selected it clears the whole set (deselect-all), otherwise it selects all
// visible rows. (A and shift+A are the same key in a terminal, so one key serves
// both select-all and deselect-all.)
func (v *runnersView) selectAllVisible() {
	if v.allVisibleSelected() {
		v.sel.clear()
	} else {
		for _, row := range v.rows {
			v.sel.add(runnerKey(row))
		}
	}
	v.applyFilter()
}

func (v runnersView) allVisibleSelected() bool {
	if len(v.rows) == 0 {
		return false
	}
	for _, row := range v.rows {
		if !v.sel.has(runnerKey(row)) {
			return false
		}
	}
	return true
}

func (v *runnersView) clearSelection() {
	v.sel.clear()
	v.applyFilter()
}

// selectedRunners returns the fused rows currently in the multi-select set.
func (v runnersView) selectedRunners() []service.FusedRunner {
	var out []service.FusedRunner
	for _, row := range v.all {
		if v.sel.has(runnerKey(row)) {
			out = append(out, row)
		}
	}
	return out
}

// tableView renders just the table (the Model composes strips, banner, and the
// bottom detail/filter region around it).
func (v runnersView) tableView() string { return v.st.view() }

// bottomLine is the one-line region beneath the table: the filter line when
// filtering, else the colored one-line detail for the selection.
func (v runnersView) bottomLine() string {
	if v.flt.shown() {
		return v.flt.line(v.theme)
	}
	return v.detail()
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
	if row.HostKnown || row.DriftClass != "" {
		parts = append(parts, v.theme.driftBadge(row.DriftClass))
	}
	if vs := versionCell(row); vs != "?" {
		parts = append(parts, v.theme.Faint.Render(vs))
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

// counts returns colored online/busy/offline tallies plus drift/behind for the
// header strip.
func (v runnersView) counts() string {
	var online, offline, busy, drift, behind int
	for _, row := range v.rows {
		switch {
		case row.Runner.Busy:
			busy++
		case row.Runner.Online():
			online++
		default:
			offline++
		}
		if row.HostKnown && row.DriftClass != "" && row.DriftClass != service.ClassHealthy {
			drift++
		}
		if row.Behind {
			behind++
		}
	}
	out := fmt.Sprintf("%s  %s  %s",
		v.theme.Online.Render(fmt.Sprintf("● %d online", online)),
		v.theme.Busy.Render(fmt.Sprintf("● %d busy", busy)),
		v.theme.Offline.Render(fmt.Sprintf("● %d off", offline)),
	)
	if drift > 0 {
		out += "  " + v.theme.Busy.Render(fmt.Sprintf("⚠ %d drift", drift))
	}
	if behind > 0 {
		out += "  " + v.theme.Faint.Render(fmt.Sprintf("↓ %d behind", behind))
	}
	return out
}

// runnerKey is the stable multi-select / fusion key for a persistent runner.
func runnerKey(row service.FusedRunner) string { return row.Org + "\x00" + row.Runner.Name }

// versionCell renders the VERSION table cell: recorded agent version (or "?" when
// unrecorded), with a trailing down-arrow when behind the published version.
func versionCell(row service.FusedRunner) string {
	v := orQ(row.AgentVersion)
	if row.Behind {
		v += " ↓"
	}
	return v
}

// driftCell renders the DRIFT table cell as plain "<glyph> <label>" (monochrome;
// color lives in the detail line). Unaudited until the host tier loads.
func driftCell(class string, hostKnown bool) string {
	if class == "" && !hostKnown {
		return "· unaudited"
	}
	g, l := driftGlyph(class)
	return g + " " + l
}

func osCell(os string) string {
	if os == "" {
		return "-"
	}
	return strings.ToLower(os)
}

// groupCell shows the runner group name, falling back to the id, then "-".
func groupCell(name string, id int64) string {
	if name != "" {
		return name
	}
	if id == 0 {
		return "-"
	}
	return strconv.FormatInt(id, 10)
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func labelString(labels []core.Label) string {
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return strings.Join(names, ",")
}
