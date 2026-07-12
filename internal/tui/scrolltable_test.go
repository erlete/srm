package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/table"
	lipgloss "charm.land/lipgloss/v2"
)

// newTestScrollTable builds a scrollTable with `rows` rows and a data window of
// `visH` visible rows (height budget = visH + the 2-line header).
func newTestScrollTable(rows, visH int) scrollTable {
	cols := []table.Column{{Title: "K", Width: 4}, {Title: "V", Width: 8}}
	st := newScrollTable(cols, NewTheme().Table)
	st.setWidth(20)
	st.setHeight(visH + 2)
	tr := make([]table.Row, rows)
	for i := range tr {
		tr[i] = table.Row{fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i)}
	}
	st.setRows(tr)
	return st
}

// The wrapper freezes the header and renders exactly the visible-window height of
// data, regardless of the (larger) full row count.
func TestScrollTableWindowsData(t *testing.T) {
	st := newTestScrollTable(20, 5)
	if !strings.Contains(stripANSI(st.header), "K") {
		t.Errorf("frozen header lost its column title: %q", stripANSI(st.header))
	}
	if got := lipgloss.Height(st.view()); got != 7 { // 2 header + 5 data
		t.Errorf("view height = %d, want 7 (2 header + 5 data window)", got)
	}
	if st.yOffset() != 0 {
		t.Errorf("initial yOffset = %d, want 0", st.yOffset())
	}
	if len(st.rows()) != 20 {
		t.Errorf("full row count = %d, want 20 (all rows retained)", len(st.rows()))
	}
}

// Cursor-follow keeps the selected row inside the window: jumping to the last row
// scrolls so it is the bottom visible row; returning to the top scrolls back.
func TestScrollTableCursorFollow(t *testing.T) {
	st := newTestScrollTable(20, 5)
	st.setCursor(19)
	if st.yOffset() != 15 { // window shows rows 15..19
		t.Errorf("yOffset after jump to last = %d, want 15", st.yOffset())
	}
	st.setCursor(0)
	if st.yOffset() != 0 {
		t.Errorf("yOffset back at top = %d, want 0", st.yOffset())
	}
	// A cursor already inside the window does not scroll.
	st.setCursor(3)
	if st.yOffset() != 0 {
		t.Errorf("yOffset for an in-window cursor = %d, want 0", st.yOffset())
	}
}

// rowAt maps a window line to an exact row using the owned offset, and rejects lines
// that land on no row.
func TestScrollTableRowAt(t *testing.T) {
	st := newTestScrollTable(20, 5)
	st.setCursor(19) // yOffset 15
	if got := st.rowAt(0); got != 15 {
		t.Errorf("rowAt(0) = %d, want 15", got)
	}
	if got := st.rowAt(4); got != 19 {
		t.Errorf("rowAt(4) = %d, want 19", got)
	}
	if got := st.rowAt(5); got != -1 { // at/below the window height (5 rows: lines 0..4)
		t.Errorf("rowAt(5) = %d, want -1", got)
	}
	if got := st.rowAt(-1); got != -1 {
		t.Errorf("rowAt(-1) = %d, want -1", got)
	}
}

// A click BELOW the visible window (the detail strip / footer sit there) must not map
// to an off-screen row even when the table is scrolled and more rows exist below.
func TestScrollTableRowAtBelowWindowIgnored(t *testing.T) {
	st := newTestScrollTable(100, 5) // 100 rows, 5-row window
	st.setCursor(0)                  // scrolled to top: rows 0..4 visible, 95 below
	// windowLine 5 = the first line below the window; without the height bound this
	// would map to row 5 (5 + yOffset 0) since 5 < 100.
	if got := st.rowAt(5); got != -1 {
		t.Errorf("rowAt(5) just below the window = %d, want -1 (off-screen row leak)", got)
	}
	if got := st.rowAt(7); got != -1 {
		t.Errorf("rowAt(7) below the window = %d, want -1", got)
	}
	if got := st.rowAt(4); got != 4 { // last visible line still maps
		t.Errorf("rowAt(4) last visible = %d, want 4", got)
	}
}

// A row count that fits within the window never scrolls, and every line maps 1:1.
func TestScrollTableFitsNoScroll(t *testing.T) {
	st := newTestScrollTable(3, 10)
	st.setCursor(2)
	if st.yOffset() != 0 {
		t.Errorf("a table that fits should not scroll, yOffset = %d", st.yOffset())
	}
	if got := st.rowAt(2); got != 2 {
		t.Errorf("rowAt(2) = %d, want 2", got)
	}
	if got := st.rowAt(3); got != -1 { // only 3 rows
		t.Errorf("rowAt(3) beyond rows = %d, want -1", got)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc && r == 'm':
			esc = false
		case !esc:
			b.WriteRune(r)
		}
	}
	return b.String()
}
