package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/erlete/srm/internal/core"
)

// repoPicker is the autocomplete multi-select for a runner group's "Repository
// access", mimicking the GitHub web control. huh has no autocomplete-multiselect,
// so this is a custom component: a search box that filters the org's repos live, a
// candidate list you toggle with tab, and a selected-set chip area pre-populated
// from the group's current repos. enter saves the diff; esc cancels.
type repoPicker struct {
	theme     Theme
	org       string
	groupID   int64
	groupName string

	search  textinput.Model
	loading bool
	loadErr error

	all      []core.Repo      // every org repo (candidate universe)
	filtered []core.Repo      // candidates matching the search
	selected map[int64]string // selected repo id -> full name
	cursor   int
}

func newRepoPicker(t Theme, org string, groupID int64, name string) repoPicker {
	ti := textinput.New()
	ti.Placeholder = "type to filter repositories…"
	ti.Prompt = "/ "
	ti.Focus()
	return repoPicker{
		theme: t, org: org, groupID: groupID, groupName: name,
		search: ti, loading: true, selected: map[int64]string{},
	}
}

// setData populates the candidate universe and pre-selects the group's current
// repos (called when the async load completes).
func (p *repoPicker) setData(all, current []core.Repo) {
	p.all = all
	for _, r := range current {
		p.selected[r.ID] = r.FullName
	}
	p.loading = false
	p.filter()
}

// filter recomputes the visible candidates from the search query.
func (p *repoPicker) filter() {
	q := strings.ToLower(strings.TrimSpace(p.search.Value()))
	p.filtered = p.filtered[:0]
	for _, r := range p.all {
		if q == "" || strings.Contains(strings.ToLower(r.FullName), q) {
			p.filtered = append(p.filtered, r)
		}
	}
	if p.cursor >= len(p.filtered) {
		p.cursor = len(p.filtered) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// update handles navigation/toggle keys and feeds everything else to the search
// box (enter/esc are handled by the Model: save / cancel). space toggles the cursor
// candidate - repo full names never contain spaces, so intercepting it costs the
// search nothing.
func (p repoPicker) update(msg tea.Msg) (repoPicker, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "up":
			if p.cursor > 0 {
				p.cursor--
			}
			return p, nil
		case "down":
			if p.cursor < len(p.filtered)-1 {
				p.cursor++
			}
			return p, nil
		case "space", " ":
			// bbt reports the space key as "space" (not " "); match both so the
			// filter never swallows it. Repo names never contain spaces, so
			// reserving space for toggle costs the search nothing.
			p.toggleCursor()
			return p, nil
		}
	}
	var cmd tea.Cmd
	p.search, cmd = p.search.Update(msg)
	p.filter()
	return p, cmd
}

// moveCursor shifts the candidate cursor by delta, clamped to the visible set.
// Used by the mouse wheel (arrow keys go through update).
func (p *repoPicker) moveCursor(delta int) {
	p.cursor += delta
	if p.cursor > len(p.filtered)-1 {
		p.cursor = len(p.filtered) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// toggleCursor flips the cursor candidate in/out of the selected set.
func (p *repoPicker) toggleCursor() {
	if p.cursor < 0 || p.cursor >= len(p.filtered) {
		return
	}
	r := p.filtered[p.cursor]
	if _, ok := p.selected[r.ID]; ok {
		delete(p.selected, r.ID)
	} else {
		p.selected[r.ID] = r.FullName
	}
}

// selectedIDs returns the selected repository ids (the SetGroupRepos payload).
func (p repoPicker) selectedIDs() []int64 {
	ids := make([]int64, 0, len(p.selected))
	for id := range p.selected {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (p repoPicker) view(width, height int) string {
	t := p.theme
	rows := []string{
		t.ModalT.Render("Repository access") + t.Faint.Render("  ·  group "+p.groupName),
	}
	if p.loading {
		rows = append(rows, "", t.Help.Render(t.Spin.Render("◐")+" loading repositories…"))
		box := t.Modal.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
	}
	if p.loadErr != nil {
		rows = append(rows, "", t.StatusErr.Render("✗ "+p.loadErr.Error()))
		box := t.Modal.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
	}

	rows = append(rows, p.search.View())
	rows = append(rows, t.Faint.Render(fmt.Sprintf("%d selected · %d shown / %d total", len(p.selected), len(p.filtered), len(p.all))))
	rows = append(rows, "")

	// Candidate list (windowed around the cursor).
	const visible = 10
	start := 0
	if p.cursor >= visible {
		start = p.cursor - visible + 1
	}
	end := start + visible
	if end > len(p.filtered) {
		end = len(p.filtered)
	}
	if len(p.filtered) == 0 {
		rows = append(rows, t.Faint.Render("  no repositories match"))
	}
	for i := start; i < end; i++ {
		r := p.filtered[i]
		mark := "[ ]"
		if _, ok := p.selected[r.ID]; ok {
			mark = "[x]"
		}
		line := mark + " " + r.FullName
		if i == p.cursor {
			line = t.Title.Render("> " + line)
		} else {
			line = "  " + t.Crumb.Render(line)
		}
		rows = append(rows, line)
	}

	rows = append(rows, "", t.Help.Render("type filter · ↑/↓ move · space toggle · enter save · esc cancel"))
	box := t.Modal.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
