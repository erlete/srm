package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/core"
)

func TestRepoPickerFilterAndSelect(t *testing.T) {
	p := newRepoPicker(NewTheme(), "acme", 5, "ci")
	p.setData(
		[]core.Repo{
			{ID: 1, FullName: "acme/api"},
			{ID: 2, FullName: "acme/web"},
			{ID: 3, FullName: "acme/api-gateway"},
		},
		[]core.Repo{{ID: 2, FullName: "acme/web"}}, // web pre-selected
	)

	if len(p.filtered) != 3 {
		t.Fatalf("unfiltered candidates = %d, want 3", len(p.filtered))
	}
	if _, ok := p.selected[2]; !ok {
		t.Error("web (id 2) should be pre-selected from current repos")
	}

	// Filter to "api".
	p.search.SetValue("api")
	p.filter()
	if len(p.filtered) != 2 {
		t.Fatalf("filtered to api = %d, want 2 (api + api-gateway)", len(p.filtered))
	}

	// Toggle the cursor (acme/api) into the selection.
	p.cursor = 0
	p.toggleCursor()
	if _, ok := p.selected[p.filtered[0].ID]; !ok {
		t.Error("toggled candidate should be selected")
	}
	// Toggle off.
	p.toggleCursor()
	if _, ok := p.selected[p.filtered[0].ID]; ok {
		t.Error("re-toggled candidate should be deselected")
	}

	ids := p.selectedIDs()
	if len(ids) != 1 || ids[0] != 2 {
		t.Errorf("selectedIDs = %v, want [2]", ids)
	}
}

// In the repo picker the filter textinput is focused, so the space key must be
// intercepted as a selection toggle through update() and NOT leak into the
// filter value. bbt reports the space key with String()=="space" (its Text is
// " "), so a literal case " " misses it. This drives the real key path (unlike
// the test above, which calls toggleCursor directly) and guards the regression
// where filtering then pressing space appended spaces to the query.
func TestRepoPickerSpaceTogglesNotTypes(t *testing.T) {
	p := newRepoPicker(NewTheme(), "acme", 5, "ui")
	p.setData([]core.Repo{
		{ID: 1, FullName: "acme/api"},
		{ID: 2, FullName: "acme/ui"},
		{ID: 3, FullName: "acme/ui-core"},
	}, nil)

	// Type a filter: "ui" (cursor lands on acme/ui).
	for _, r := range []rune("ui") {
		p, _ = p.update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := p.search.Value(); got != "ui" {
		t.Fatalf("filter value after typing = %q, want %q", got, "ui")
	}

	// Space toggles the highlighted candidate; it must not append to the filter.
	p, _ = p.update(tea.KeyPressMsg{Code: ' ', Text: " "})

	if got := p.search.Value(); got != "ui" {
		t.Errorf("space leaked into the filter: value = %q, want %q", got, "ui")
	}
	if len(p.selected) != 1 {
		t.Fatalf("space did not toggle a selection: %d selected, want 1", len(p.selected))
	}
	if _, ok := p.selected[2]; !ok {
		t.Errorf("space toggled the wrong repo: selected=%v, want id 2 (acme/ui)", p.selected)
	}

	// Space again clears it (toggle off).
	p, _ = p.update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if len(p.selected) != 0 {
		t.Errorf("second space did not clear the selection: %d selected, want 0", len(p.selected))
	}
}
