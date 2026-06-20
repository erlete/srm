package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/table"
)

// The selected row's background must span the WHOLE row, not just the first
// column. bubbles emits an ANSI reset ("\x1b[0m") at each cell boundary when a
// cell carries styling; those resets truncate the row-level Selected background.
// With cells uncolored, the selected row should contain no interior reset — only
// the trailing one — so the background runs the full width.
func TestSelectedRowSpansFullWidth(t *testing.T) {
	th := NewTheme()
	cols := fillWidth([]table.Column{{Title: "ORG", Width: 8}, {Title: "NAME", Width: 8}}, 40)
	tbl := table.New(table.WithColumns(cols), table.WithFocused(true))
	tbl.SetStyles(th.Table)
	tbl.SetWidth(40)
	tbl.SetHeight(5)
	tbl.SetRows([]table.Row{{"acme", "r1"}, {"globex", "r2"}})

	out := tbl.View()
	var selLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "acme") { // cursor starts at row 0
			selLine = line
			break
		}
	}
	if selLine == "" {
		t.Fatal("could not find the selected row in table output")
	}
	t.Logf("selected row: %q", selLine)

	const reset = "\x1b[0m"
	// A reset before the last column's content would clip the background early.
	if i := strings.Index(selLine, reset); i != -1 {
		if after := selLine[i+len(reset):]; strings.Contains(after, "r1") {
			t.Errorf("selected-row background is reset before the last column — highlight won't span the full row; line=%q", selLine)
		}
	}
}
