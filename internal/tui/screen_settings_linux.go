//go:build !windows

package tui

import "github.com/erlete/srm/internal/config"

// capLines renders the per-runner cgroup caps panel plus, in auto mode, the aggregate
// slice ceiling - the Linux capacity model. (Windows uses a Job Object on ephemeral
// jobs; see screen_settings_windows.go.)
func (v settingsView) capLines(kv func(string, string) string, orDash func(string) string) []string {
	t, s := v.theme, v.snap
	lines := []string{
		t.PanelTtl.Render("Per-runner caps (each runner's cgroup)"),
		kv("MemoryHigh", orDash(s.Effective.MemoryHigh)),
		kv("MemoryMax", orDash(s.Effective.MemoryMax)),
		kv("MemorySwapMax", orDash(s.Effective.MemorySwapMax)),
		kv("CPUWeight", orDash(s.Effective.CPUWeight)),
		kv("TasksMax", orDash(s.Effective.TasksMax)),
	}
	if s.Mode == config.ResourceModeAuto {
		lines = append(lines,
			"",
			t.PanelTtl.Render("Aggregate ceiling (all runners combined)"),
			kv("srm.slice Max", s.SliceMax),
		)
	}
	return lines
}

// offModeHint completes the "off" capacity-mode crumb; capNoteDesc is the edit-form note.
const (
	offModeHint = " - jobs are UNBOUNDED; switch to auto to OOM-proof"
	capNoteDesc = "Empty field = pre-packaged auto default (shown as placeholder) in Auto mode, or unbounded in Manual mode."
)
