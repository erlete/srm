package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/service"
)

// ephemeralView is the ephemeral slot inventory: a host-local table of slot lanes
// judged by HOST health alone (active + not crash-looping + conformant unit). It
// is a deliberately SEPARATE panel from the persistent runners view - the two
// natures must never be mistaken: here you scale slots and drain by slot id, never
// address a runner by name. It now surfaces the live cgroup memory (LIVE) and
// OOM-kill count the service already collected but the table used to hide.
type ephemeralView struct {
	tbl   table.Model
	cols  []table.Column      // base column widths (re-fitted to the terminal on resize)
	all   []service.FusedSlot // full set
	rows  []service.FusedSlot // filtered (mirrors the table rows)
	theme Theme
	width int
	flt   filterState
	sel   selectionSet
}

func newEphemeralView(t Theme) ephemeralView {
	cols := []table.Column{
		{Title: "", Width: 3}, // multi-select gutter
		{Title: "ORG", Width: 16},
		{Title: "SLOT", Width: 6},
		{Title: "GROUP", Width: 9},
		{Title: "STATE", Width: 13},
		{Title: "RESTARTS", Width: 9},
		{Title: "LIVE", Width: 9},
		{Title: "MEM peak/cap", Width: 15},
		{Title: "OOM", Width: 5},
	}
	tbl := table.New(table.WithColumns(cols), table.WithFocused(true))
	tbl.SetStyles(t.Table)
	return ephemeralView{tbl: tbl, cols: cols, theme: t, sel: newSelectionSet(),
		flt: newFilterState("type to filter by org / slot / state…")}
}

func (v *ephemeralView) setSize(w, h int) {
	v.width = w
	v.tbl.SetWidth(w)
	v.tbl.SetColumns(fillWidth(v.cols, w))
	v.flt.setWidth(w - 4)
	if h > 3 {
		v.tbl.SetHeight(h)
	}
}

func (v *ephemeralView) setRows(rows []service.FusedSlot) {
	v.all = rows
	v.applyFilter()
}

// applyFilter recomputes the visible slot rows, preserving cursor position.
func (v *ephemeralView) applyFilter() {
	cursor := v.tbl.Cursor()
	q := v.flt.query()
	v.rows = v.rows[:0]
	for _, s := range v.all {
		if q == "" || strings.Contains(v.haystack(s), q) {
			v.rows = append(v.rows, s)
		}
	}
	tr := make([]table.Row, 0, len(v.rows))
	for _, s := range v.rows {
		tr = append(tr, table.Row{
			v.sel.gutter(slotKey(s)), s.Org, s.Slot, groupCell(s.GroupName, s.GroupID), slotStatePlain(s.EphemeralSlot),
			fmt.Sprintf("%d", s.Restarts), humanBytes(s.MemCur), memPair(s.MemPeak, s.MemMax),
			oomCell(s.OOMKills),
		})
	}
	v.tbl.SetRows(tr)
	if cursor >= len(tr) {
		cursor = len(tr) - 1
	}
	if cursor >= 0 {
		v.tbl.SetCursor(cursor)
	}
}

func (v ephemeralView) haystack(s service.FusedSlot) string {
	label, _ := slotState(s.EphemeralSlot)
	return strings.ToLower(strings.Join([]string{s.Org, s.Slot, label}, " "))
}

// startFilter focuses the filter input. stopFilter blurs it, optionally clearing.
func (v *ephemeralView) startFilter() tea.Cmd { return v.flt.start() }
func (v *ephemeralView) stopFilter(clear bool) {
	v.flt.stop(clear)
	if clear {
		v.applyFilter()
	}
}

func (v ephemeralView) filtering() bool { return v.flt.filtering() }

func (v ephemeralView) updateFilter(msg tea.Msg) (ephemeralView, tea.Cmd) {
	var cmd tea.Cmd
	v.flt, cmd = v.flt.update(msg)
	v.applyFilter()
	return v, cmd
}

func (v ephemeralView) update(msg tea.Msg) (ephemeralView, tea.Cmd) {
	var cmd tea.Cmd
	v.tbl, cmd = v.tbl.Update(msg)
	return v, cmd
}

func (v ephemeralView) selected() (service.FusedSlot, bool) {
	i := v.tbl.Cursor()
	if i < 0 || i >= len(v.rows) {
		return service.FusedSlot{}, false
	}
	return v.rows[i], true
}

// focusKey moves the cursor to the slot with the given key, if visible.
func (v *ephemeralView) focusKey(key string) {
	for i, s := range v.rows {
		if slotKey(s) == key {
			v.tbl.SetCursor(i)
			return
		}
	}
}

func (v *ephemeralView) toggleSelect() {
	if s, ok := v.selected(); ok {
		v.sel.toggle(slotKey(s))
		v.applyFilter()
	}
}

// selectAllVisible toggles bulk selection (select-all / deselect-all) - see the
// runnersView counterpart for why one key serves both.
func (v *ephemeralView) selectAllVisible() {
	if v.allVisibleSelected() {
		v.sel.clear()
	} else {
		for _, s := range v.rows {
			v.sel.add(slotKey(s))
		}
	}
	v.applyFilter()
}

func (v ephemeralView) allVisibleSelected() bool {
	if len(v.rows) == 0 {
		return false
	}
	for _, s := range v.rows {
		if !v.sel.has(slotKey(s)) {
			return false
		}
	}
	return true
}

func (v *ephemeralView) clearSelection() {
	v.sel.clear()
	v.applyFilter()
}

func (v ephemeralView) selectedSlots() []service.FusedSlot {
	var out []service.FusedSlot
	for _, s := range v.all {
		if v.sel.has(slotKey(s)) {
			out = append(out, s)
		}
	}
	return out
}

func (v ephemeralView) tableView() string { return v.tbl.View() }

func (v ephemeralView) bottomLine() string {
	if v.flt.shown() {
		return v.flt.line(v.theme)
	}
	return v.detail()
}

func (v ephemeralView) detail() string {
	if len(v.rows) == 0 {
		if v.flt.shown() {
			return v.theme.Help.Render("no slots match")
		}
		return v.theme.Help.Render("no ephemeral slots on this host - press n to add some")
	}
	s, ok := v.selected()
	if !ok {
		return v.theme.Help.Render("no slot selected")
	}
	parts := []string{
		v.theme.Title.Render("slot " + s.Slot),
		v.theme.Crumb.Render(s.Org),
		v.slotBadge(s.EphemeralSlot),
		v.theme.Faint.Render(fmt.Sprintf("%d restarts", s.Restarts)),
	}
	if s.OOMKills > 0 {
		parts = append(parts, v.theme.Offline.Render(fmt.Sprintf("⚠ %d OOM-kill(s)", s.OOMKills)))
	}
	if m := memPair(s.MemPeak, s.MemMax); m != "-" {
		parts = append(parts, v.theme.Faint.Render("mem "+m))
	}
	return strings.Join(parts, v.theme.Faint.Render(" · "))
}

// slotState classifies a slot lane exactly as reconcile does (host health only).
func slotState(s service.EphemeralSlot) (label string, healthy bool) {
	switch {
	case s.Active && s.Restarts < service.EphemeralRestartThreshold && s.UnitOK:
		return "active", true
	case !s.Active:
		return "down", false
	case !s.UnitOK:
		return "drift", false
	default:
		return "crash-loop", false
	}
}

func slotStatePlain(s service.EphemeralSlot) string {
	label, healthy := slotState(s)
	mark := "▲"
	if healthy {
		mark = "●"
	} else if label == "down" {
		mark = "○"
	}
	return mark + " " + label
}

func (v ephemeralView) slotBadge(s service.EphemeralSlot) string {
	label, healthy := slotState(s)
	switch {
	case healthy:
		return v.theme.Online.Render("● " + label)
	case label == "down":
		return v.theme.Offline.Render("○ " + label)
	default:
		return v.theme.Busy.Render("▲ " + label)
	}
}

// counts returns colored healthy/down/issue tallies plus an OOM sum for the header.
func (v ephemeralView) counts() string {
	var healthy, down, issue int
	var oom int64
	for _, s := range v.rows {
		label, ok := slotState(s.EphemeralSlot)
		switch {
		case ok:
			healthy++
		case label == "down":
			down++
		default:
			issue++
		}
		if s.OOMKills > 0 {
			oom += s.OOMKills
		}
	}
	out := fmt.Sprintf("%s  %s  %s",
		v.theme.Online.Render(fmt.Sprintf("● %d active", healthy)),
		v.theme.Offline.Render(fmt.Sprintf("○ %d down", down)),
		v.theme.Busy.Render(fmt.Sprintf("▲ %d issue", issue)),
	)
	if oom > 0 {
		out += "  " + v.theme.Offline.Render(fmt.Sprintf("⚠ %d OOM", oom))
	}
	return out
}

// slotKey is the stable multi-select / fusion key for an ephemeral slot.
func slotKey(s service.FusedSlot) string { return s.Org + "\x00" + s.Slot }

// oomCell renders the OOM table cell ("-" when none/unknown, else the count).
func oomCell(n int64) string {
	if n <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", n)
}

func okDrift(ok bool) string {
	if ok {
		return "ok"
	}
	return "drift"
}

// memPair formats "peak/max" cgroup memory, using "-" for unknown/unlimited sides.
func memPair(peak, max int64) string {
	p, m := humanBytes(peak), humanBytes(max)
	if p == "-" && m == "-" {
		return "-"
	}
	return p + "/" + m
}

// humanBytes renders a byte count as a compact human string; -1 (unknown/
// unlimited) becomes "-".
func humanBytes(n int64) string {
	if n < 0 {
		return "-"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}
