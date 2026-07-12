package tui

import (
	"strings"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// scrollTable wraps a bubbles/table.Model to give reliable per-row mouse mapping.
// bubbles/table hides its scroll offset (unexported start/end + an internal viewport
// YOffset), so a click cannot be mapped to a row from the outside. This wrapper sizes
// the table to its FULL row count so it never scrolls internally (every row renders,
// header + all data), then splits the render into a frozen header and the data lines,
// and windows the data through an OWNED viewport whose offset we control. Because we
// own the offset, `rowAt` maps a click to an exact row. Keyboard/wheel still move the
// table cursor (identical to before); cursor-follow scrolls the owned viewport to keep
// the selected row visible. The four fleet tables share it.
type scrollTable struct {
	tbl     table.Model
	vp      viewport.Model
	header  string // cached frozen header lines (recomputed each sync)
	headerH int    // header height in lines (measured; 2 for the themed tables)
	targetH int    // total height budget for the table region (header + data)
	width   int    // render width; the composed output is clipped to it (see view)
}

func newScrollTable(cols []table.Column, styles table.Styles) scrollTable {
	tbl := table.New(table.WithColumns(cols), table.WithFocused(true))
	tbl.SetStyles(styles)
	vp := viewport.New()
	// The Model routes the wheel to a cursor move (unchanged behavior), so the
	// viewport does not also scroll on the wheel.
	vp.MouseWheelEnabled = false
	return scrollTable{tbl: tbl, vp: vp, headerH: 2}
}

func (s *scrollTable) setColumns(cols []table.Column) {
	s.tbl.SetColumns(cols)
	s.sync()
}

func (s *scrollTable) setWidth(w int) {
	s.width = w
	s.tbl.SetWidth(w)
	s.vp.SetWidth(w)
	s.sync()
}

// setHeight sets the total height budget for the table region (frozen header + the
// scrollable data window), matching the old tbl.SetHeight(h) contract.
func (s *scrollTable) setHeight(h int) {
	s.targetH = h
	s.sync()
}

func (s *scrollTable) setRows(rows []table.Row) {
	s.tbl.SetRows(rows)
	s.sync()
}

func (s scrollTable) rows() []table.Row { return s.tbl.Rows() }
func (s scrollTable) cursor() int       { return s.tbl.Cursor() }

func (s *scrollTable) setCursor(n int) {
	s.tbl.SetCursor(n)
	s.sync()
}

// moveUp / moveDown move the cursor (keyboard arrows and the wheel), then follow it.
func (s *scrollTable) moveUp(n int) {
	s.tbl.MoveUp(n)
	s.sync()
}

func (s *scrollTable) moveDown(n int) {
	s.tbl.MoveDown(n)
	s.sync()
}

// update forwards a message (keyboard nav) to the underlying table, then re-syncs so
// the owned viewport follows the new cursor. Keeps keyboard behavior identical.
func (s *scrollTable) update(msg tea.Msg) tea.Cmd {
	tbl, cmd := s.tbl.Update(msg)
	s.tbl = tbl
	s.sync()
	return cmd
}

// yOffset is the index of the first visible data row (what the click math adds).
func (s scrollTable) yOffset() int { return s.vp.YOffset() }

// headerHeight is the frozen-header line count (the offset from the table's top to
// its first data row), used by the Model's click->row mapping.
func (s scrollTable) headerHeight() int { return s.headerH }

// rowAt maps a data-window line (screen Y minus the first data row's screen Y) to a
// row index, or -1 when it lands on no row. The window-height bound is essential: the
// data window is exactly vp.Height() lines, and the detail/selection strip and footer
// render immediately BELOW it at the same left edge, so without this a click there
// (windowLine >= the window height) would otherwise map to a real off-screen row when
// the table is scrolled - moving the cursor to, and even gutter-toggling, a runner the
// operator never pointed at.
func (s *scrollTable) rowAt(windowLine int) int {
	if windowLine < 0 || windowLine >= s.vp.Height() {
		return -1
	}
	idx := windowLine + s.vp.YOffset()
	if idx < 0 || idx >= len(s.tbl.Rows()) {
		return -1
	}
	return idx
}

// view renders the frozen header above the windowed data viewport, clipped to the
// render width. The clip matters: the themed table renders its header border at the
// column-sum width (a few columns over the viewport width), and without clipping the
// over-wide header wraps in the Model's outer width box, inserting a phantom line that
// throws off the click->row mapping. The data lines are clipped for the same reason.
func (s scrollTable) view() string {
	body := lipgloss.JoinVertical(lipgloss.Left, s.header, s.vp.View())
	if s.width > 0 {
		body = lipgloss.NewStyle().MaxWidth(s.width).Render(body)
	}
	return body
}

// sync re-renders the table at full height (so every row renders with no internal
// scroll), splits off the frozen header, loads the data lines into the owned
// viewport, sizes the window, and scrolls it to keep the cursor visible.
func (s *scrollTable) sync() {
	// Measure the header height from a live render (title + bottom border = 2 for the
	// themed tables). tbl.View() always emits headerH + tbl.Height() lines (the inner
	// viewport pads to its height), so the difference is the header height.
	if s.tbl.Height() > 0 {
		if h := lipgloss.Height(s.tbl.View()) - s.tbl.Height(); h >= 1 {
			s.headerH = h
		}
	}
	// Full-height render: viewport height = row count, so all rows render, unpadded.
	n := len(s.tbl.Rows())
	s.tbl.SetHeight(n + s.headerH)

	lines := strings.Split(s.tbl.View(), "\n")
	if len(lines) >= s.headerH {
		s.header = strings.Join(lines[:s.headerH], "\n")
		s.vp.SetContent(strings.Join(lines[s.headerH:], "\n"))
	} else {
		s.header = strings.Join(lines, "\n")
		s.vp.SetContent("")
	}

	visH := s.targetH - s.headerH
	if visH < 1 {
		visH = 1
	}
	s.vp.SetHeight(visH)
	s.follow()
}

// follow scrolls the owned viewport so the cursor row stays within the visible window.
func (s *scrollTable) follow() {
	cur := s.tbl.Cursor()
	if cur < 0 {
		return
	}
	visH := s.vp.Height()
	if visH <= 0 {
		return
	}
	off := s.vp.YOffset()
	switch {
	case cur < off:
		s.vp.SetYOffset(cur)
	case cur >= off+visH:
		s.vp.SetYOffset(cur - visH + 1)
	}
}
