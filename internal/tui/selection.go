package tui

// selectionSet is the multi-select state for a list view (BACKLOG #1 bulk ops). It
// holds a set of selected row KEYS - stable org-scoped ids that survive filtering
// and scrolling - rather than table indices, which shift as rows are filtered. The
// leftmost [x]/[ ] gutter column and the "selected: N" chip read from it; a bulk
// op acts on the whole set in one streamed pass, then clears it.
type selectionSet struct {
	keys map[string]bool
}

func newSelectionSet() selectionSet { return selectionSet{keys: map[string]bool{}} }

func (s *selectionSet) toggle(key string) {
	if s.keys == nil {
		s.keys = map[string]bool{}
	}
	if s.keys[key] {
		delete(s.keys, key)
	} else {
		s.keys[key] = true
	}
}

func (s *selectionSet) add(keys ...string) {
	if s.keys == nil {
		s.keys = map[string]bool{}
	}
	for _, k := range keys {
		s.keys[k] = true
	}
}

func (s selectionSet) has(key string) bool { return s.keys[key] }
func (s selectionSet) count() int          { return len(s.keys) }
func (s *selectionSet) clear()             { s.keys = map[string]bool{} }

// gutter renders the leftmost selection cell for a row key.
func (s selectionSet) gutter(key string) string {
	if s.has(key) {
		return "[x]"
	}
	return "[ ]"
}
