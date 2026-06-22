//go:build windows

package tui

// capLines renders the Windows Job-Object caps panel: only MemoryMax + TasksMax have a
// Job-Object analog, and caps apply to EPHEMERAL jobs only (the supervisor holds the
// handle; persistent SCM-service runners are unbounded). There is no host-wide slice.
// (Linux's per-runner cgroup model is in screen_settings_linux.go.)
func (v settingsView) capLines(kv func(string, string) string, orDash func(string) string) []string {
	t, s := v.theme, v.snap
	return []string{
		t.PanelTtl.Render("Resource caps (ephemeral jobs, Windows Job Object)"),
		kv("MemoryMax", orDash(s.Effective.MemoryMax)),
		kv("TasksMax", orDash(s.Effective.TasksMax)),
		t.Faint.Render("  (persistent runners run unbounded on Windows)"),
	}
}

// offModeHint completes the "off" capacity-mode crumb; capNoteDesc is the edit-form note.
const (
	offModeHint = " - jobs are UNBOUNDED; switch to auto to cap ephemeral jobs"
	capNoteDesc = "Empty field = pre-packaged auto default (shown as placeholder) in Auto mode, " +
		"or unbounded in Manual mode. On Windows only MemoryMax + TasksMax take effect, and only " +
		"for ephemeral jobs (Job Object); the other fields are stored but ignored, and there is no " +
		"host-wide srm.slice."
)
