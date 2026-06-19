package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/erlete/srm/internal/service"
)

// ephemeralView is the ephemeral slot inventory: a host-local table of slot lanes
// (ORG/SLOT columns) judged by HOST health alone. It is a deliberately SEPARATE
// panel from the persistent runners view — the two natures must never be mistaken:
// here you scale slots and drain by slot id, never address a runner by name.
type ephemeralView struct {
	tbl   table.Model
	cols  []table.Column // base column widths (re-fitted to the terminal on resize)
	rows  []service.EphemeralSlot
	theme Theme
	width int
}

func newEphemeralView(t Theme) ephemeralView {
	cols := []table.Column{
		{Title: "ORG", Width: 16},
		{Title: "SLOT", Width: 6},
		{Title: "STATE", Width: 14},
		{Title: "RESTARTS", Width: 9},
		{Title: "CONFORM", Width: 8},
		{Title: "MEM peak/max", Width: 18},
	}
	tbl := table.New(table.WithColumns(cols), table.WithFocused(true))
	tbl.SetStyles(t.Table)
	return ephemeralView{tbl: tbl, cols: cols, theme: t}
}

func (v *ephemeralView) setSize(w, h int) {
	v.width = w
	v.tbl.SetWidth(w)
	v.tbl.SetColumns(fillWidth(v.cols, w))
	if h > 3 {
		v.tbl.SetHeight(h)
	}
}

func (v *ephemeralView) setRows(rows []service.EphemeralSlot) {
	v.rows = rows
	tr := make([]table.Row, 0, len(rows))
	for _, s := range rows {
		conform := "ok"
		if !s.UnitOK {
			conform = "drift"
		}
		tr = append(tr, table.Row{
			s.Org, s.Slot, slotStatePlain(s), fmt.Sprintf("%d", s.Restarts),
			conform, memPair(s.MemPeak, s.MemMax),
		})
	}
	v.tbl.SetRows(tr)
}

func (v ephemeralView) update(msg tea.Msg) (ephemeralView, tea.Cmd) {
	var cmd tea.Cmd
	v.tbl, cmd = v.tbl.Update(msg)
	return v, cmd
}

func (v ephemeralView) selected() (service.EphemeralSlot, bool) {
	i := v.tbl.Cursor()
	if i < 0 || i >= len(v.rows) {
		return service.EphemeralSlot{}, false
	}
	return v.rows[i], true
}

func (v ephemeralView) view() string {
	return lipgloss.JoinVertical(lipgloss.Left, v.tbl.View(), v.detail())
}

func (v ephemeralView) detail() string {
	if len(v.rows) == 0 {
		return v.theme.Help.Render("no ephemeral slots on this host — press n to add some")
	}
	s, ok := v.selected()
	if !ok {
		return v.theme.Help.Render("no slot selected")
	}
	parts := []string{
		v.theme.Title.Render("slot " + s.Slot),
		v.theme.Crumb.Render(s.Org),
		v.slotBadge(s),
		v.theme.Faint.Render(fmt.Sprintf("%d restarts", s.Restarts)),
	}
	if !s.UnitOK {
		parts = append(parts, v.theme.Busy.Render("unit drift — recreate to apply"))
	}
	if s.OOMKills > 0 {
		parts = append(parts, v.theme.Offline.Render(fmt.Sprintf("⚠ %d OOM-kill(s)", s.OOMKills)))
	}
	if m := memPair(s.MemPeak, s.MemMax); m != "—" {
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

// counts returns colored healthy/down/issue tallies for the header.
func (v ephemeralView) counts() string {
	var healthy, down, issue int
	for _, s := range v.rows {
		label, ok := slotState(s)
		switch {
		case ok:
			healthy++
		case label == "down":
			down++
		default:
			issue++
		}
	}
	return fmt.Sprintf("%s  %s  %s",
		v.theme.Online.Render(fmt.Sprintf("●%d active", healthy)),
		v.theme.Offline.Render(fmt.Sprintf("○%d down", down)),
		v.theme.Busy.Render(fmt.Sprintf("▲%d issue", issue)),
	)
}

// memPair formats "peak/max" cgroup memory, using "—" for unknown/unlimited sides.
func memPair(peak, max int64) string {
	p, m := humanBytes(peak), humanBytes(max)
	if p == "—" && m == "—" {
		return "—"
	}
	return p + "/" + m
}

// humanBytes renders a byte count as a compact human string; -1 (unknown/
// unlimited) becomes "—".
func humanBytes(n int64) string {
	if n < 0 {
		return "—"
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
