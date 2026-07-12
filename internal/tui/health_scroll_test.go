package tui

import (
	"fmt"
	"testing"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/erlete/srm/internal/service"
)

// bigHost builds a healthHost whose panel content is many lines (a long rootless-DinD
// checklist), enough to overflow a small body.
func bigHost(n int) healthHost {
	checks := make([]service.DinDCheck, n)
	for i := range checks {
		checks[i] = service.DinDCheck{Name: fmt.Sprintf("check-%d", i), OK: true}
	}
	return healthHost{
		CapacityMode: "auto",
		DinD:         service.DinDReport{Enabled: true, Checks: checks},
	}
}

// The host panel caps at the body height and becomes scrollable when its content
// overflows: the wheel/PgDn scroll it (clamped at both ends), the panel never renders
// taller than the body, and a panel that fits shows no scroll hint and never scrolls.
func TestHostPanelScroll(t *testing.T) {
	v := newHealthView(NewTheme())
	v.setSize(160, 10)             // viewport height budget = 10 - 4 = 6 lines
	v.setReports(nil, bigHost(20)) // ~26 content lines, well over the window

	if v.hostContentH <= v.hostVP.Height() {
		t.Fatalf("expected overflow: contentH=%d vpH=%d", v.hostContentH, v.hostVP.Height())
	}
	if v.scrollHint() == "" {
		t.Error("an overflowing host panel should show a scroll hint")
	}
	// The bordered panel must never render taller than the body it sits in.
	if h := lipgloss.Height(v.hostPanel(v.rightWidth())); h > v.bodyH {
		t.Errorf("host panel height %d exceeds body height %d", h, v.bodyH)
	}

	// Scrolling up at the top is a clamped no-op; a page down advances the offset.
	v.scrollHost(-5)
	if got := v.hostVP.YOffset(); got != 0 {
		t.Errorf("scroll up at the top should clamp to 0, got %d", got)
	}
	v.pageHost(1)
	if v.hostVP.YOffset() == 0 {
		t.Error("page down should advance the scroll offset")
	}
	// Scrolling far past the end clamps to the max offset (contentH - visibleH).
	v.scrollHost(1000)
	maxOff := v.hostContentH - v.hostVP.Height()
	if got := v.hostVP.YOffset(); got != maxOff {
		t.Errorf("scroll past the end should clamp to %d, got %d", maxOff, got)
	}

	// A panel whose content fits the body does not scroll and shows no hint.
	fits := newHealthView(NewTheme())
	fits.setSize(160, 40)
	fits.setReports(nil, bigHost(2))
	if fits.scrollHint() != "" {
		t.Errorf("a fitting host panel should show no scroll hint, got %q", fits.scrollHint())
	}
	fits.scrollHost(50)
	if got := fits.hostVP.YOffset(); got != 0 {
		t.Errorf("a fitting host panel should not scroll, offset = %d", got)
	}
}
