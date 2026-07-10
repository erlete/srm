package tui

import "testing"

func TestOrgFilterResult(t *testing.T) {
	orgs := []string{"acme", "globex", "initech"}

	// Fresh modal: all selected -> result is nil ("all").
	m := newOrgFilterModal(NewTheme(), orgs, nil)
	if r := m.result(); r != nil {
		t.Errorf("all-selected should yield nil (all), got %v", r)
	}

	// Deselect one -> explicit subset.
	m.cursor = 1
	m.toggle() // drop globex
	r := m.result()
	if len(r) != 2 || !r["acme"] || !r["initech"] || r["globex"] {
		t.Errorf("subset result wrong: %v", r)
	}

	// toggleAll from a subset selects all -> nil.
	m.toggleAll()
	if m.result() != nil {
		t.Error("toggleAll from subset should select all -> nil")
	}
	// toggleAll again clears to none -> also nil (showing nothing is never useful).
	m.toggleAll()
	if m.result() != nil {
		t.Errorf("none-selected should collapse to nil (all), got %v", m.result())
	}
}
