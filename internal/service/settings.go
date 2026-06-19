package service

import (
	"fmt"

	"github.com/erlete/srm/internal/config"
)

// ResourceSettings is the editable cgroup-capacity policy surfaced by the TUI
// Settings panel: the mode, the host-wide per-runner caps, and the aggregate
// slice ceiling. It mirrors the subset of config a user tunes for OOM safety —
// the rest of the config (orgs, auth, install roots) is left untouched.
type ResourceSettings struct {
	Mode      string                // "" (manual/off) or config.ResourceModeAuto
	Resources config.ResourceLimits // host-wide per-runner overrides (empty fields inherit)
	SliceMax  string                // aggregate slice ceiling (auto mode only); "" = default
}

// ResourceSettings returns the current capacity policy as shown in the TUI: the
// mode, the literal host-wide overrides, and the slice ceiling override.
func (m *Manager) ResourceSettings() ResourceSettings {
	return ResourceSettings{
		Mode:      m.cfg.ResourceMode,
		Resources: m.cfg.Resources,
		SliceMax:  m.cfg.SliceMemoryMax,
	}
}

// ApplyResourceSettings updates the in-memory capacity policy and persists it to
// the config file. The change takes effect for SUBSEQUENT orchestrator builds
// (new/recreated runners) immediately; already-installed runner units only pick
// up the new caps after `srm runners refresh`. The mode is validated so a typo
// can't silently leave jobs unbounded on the next load (mirrors config.Load).
func (m *Manager) ApplyResourceSettings(s ResourceSettings) error {
	if s.Mode != "" && s.Mode != config.ResourceModeAuto {
		return fmt.Errorf("invalid resource mode %q — use %q or empty", s.Mode, config.ResourceModeAuto)
	}
	m.cfg.ResourceMode = s.Mode
	m.cfg.Resources = s.Resources
	m.cfg.SliceMemoryMax = s.SliceMax
	return m.SaveConfig()
}
