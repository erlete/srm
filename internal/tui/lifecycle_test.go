package tui

import (
	"testing"

	"github.com/erlete/srm/internal/service"
)

func TestLifecycleCursorClamps(t *testing.T) {
	v := newLifecycleView(NewTheme())
	n := len(v.cards)
	if n == 0 {
		t.Fatal("lifecycle has no cards")
	}
	v.move(-1)
	if v.cursor != 0 {
		t.Errorf("cursor underflowed to %d, want 0", v.cursor)
	}
	for i := 0; i < n+5; i++ {
		v.move(1)
	}
	if v.cursor != n-1 {
		t.Errorf("cursor overflowed to %d, want %d", v.cursor, n-1)
	}
	// The uninstall card is host-gated; the backup card is not.
	if !v.cards[0].enabled {
		t.Error("backup card should be enabled")
	}
}

func TestUninstallBlastLines(t *testing.T) {
	rep := service.UninstallReport{
		Persistent: []string{"acme/r-1"},
		Ephemeral:  []string{"acme/1"},
		Users:      []string{"srm-acme"},
		Paths:      []string{"/opt/actions-runners/acme"},
		Skipped:    []string{"foreign unit x"},
	}
	lines := uninstallBlastLines(NewTheme(), rep)
	if len(lines) != 5 {
		t.Fatalf("blast lines = %d, want 5", len(lines))
	}
}
