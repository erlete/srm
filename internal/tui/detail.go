package tui

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/erlete/srm/internal/service"
)

// driftGlyph maps a reconcile drift class to a one-glyph badge + short label for
// the DRIFT column and inline badges. The label is the canonical service.ClassLabel
// (the SINGLE vocab shared with the CLI, item 6 #5); only the glyph - pure terminal
// presentation - lives here. An empty class is "unaudited" (the host tier has not
// loaded) - never asserted healthy.
func driftGlyph(class string) (glyph, label string) {
	return driftClassGlyph(class), service.ClassLabel(class)
}

// driftClassGlyph is the per-class badge glyph (presentation only).
func driftClassGlyph(class string) string {
	switch class {
	case service.ClassHealthy, service.ClassEphemeralSlot:
		return "●"
	case service.ClassStaleDropIn, service.ClassOrphanUnit, service.ClassLegacyFlat:
		return "⚠"
	case service.ClassDropInNewer, service.ClassEphemeralNewer:
		return "↑"
	case service.ClassStuck, service.ClassEphemeralStuck:
		return "▲"
	case service.ClassOrphanGitHub:
		return "○"
	case service.ClassUnknown:
		return "?"
	case "":
		return "·"
	default:
		return "·"
	}
}

// driftStyle is the color for a drift class badge, by severity band.
func (t Theme) driftStyle(class string) lipgloss.Style {
	switch class {
	case service.ClassHealthy, service.ClassEphemeralSlot:
		return t.Online
	case service.ClassStaleDropIn, service.ClassLegacyFlat:
		return t.Busy
	case service.ClassDropInNewer, service.ClassEphemeralNewer:
		return t.Newer
	case service.ClassStuck, service.ClassEphemeralStuck, service.ClassOrphanUnit:
		return t.Offline
	default: // orphan-github, unknown, unaudited
		return t.Faint
	}
}

// driftBadge renders the colored "<glyph> <label>" badge for a class.
func (t Theme) driftBadge(class string) string {
	g, l := driftGlyph(class)
	return t.driftStyle(class).Render(g + " " + l)
}

// sparkRunes are the eight block heights a sparkline draws with, lowest first.
var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// sparkline renders samples as block runes scaled to max (e.g. the cgroup cap), so
// the height of each column is that sample's share of the ceiling. When max is
// unknown (<=0) it scales to the largest sample instead, so the shape still shows
// (just not the absolute utilization). Zero-deps, one rune per sample.
func sparkline(samples []int64, max int64) string {
	if len(samples) == 0 {
		return ""
	}
	scale := max
	if scale <= 0 {
		for _, s := range samples {
			if s > scale {
				scale = s
			}
		}
	}
	if scale <= 0 {
		return strings.Repeat(string(sparkRunes[0]), len(samples))
	}
	top := int64(len(sparkRunes) - 1)
	var b strings.Builder
	for _, s := range samples {
		if s < 0 {
			s = 0
		}
		idx := top * s / scale
		if idx < 0 {
			idx = 0
		}
		if idx > top {
			idx = top
		}
		b.WriteRune(sparkRunes[idx])
	}
	return b.String()
}

// bar renders a fixed-width [▓▓░░] meter colored by utilization band (green < 70,
// amber 70-90, red > 90). frac is clamped to [0,1].
func bar(frac float64, width int, t Theme) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac*float64(width) + 0.5)
	style := t.Online
	switch {
	case frac > 0.90:
		style = t.Offline
	case frac >= 0.70:
		style = t.Busy
	}
	return style.Render(strings.Repeat("▓", filled)) + t.Faint.Render(strings.Repeat("░", width-filled))
}
