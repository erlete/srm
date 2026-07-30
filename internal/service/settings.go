package service

import (
	"fmt"
	"strings"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
)

// ResourceSettings is the editable cgroup-capacity policy surfaced by the TUI
// Settings panel: the mode, the host-wide per-runner caps, and the aggregate
// slice ceiling. It mirrors the subset of config a user tunes for OOM safety -
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
		return fmt.Errorf("invalid resource mode %q - use %q or empty", s.Mode, config.ResourceModeAuto)
	}
	m.cfg.ResourceMode = s.Mode
	m.cfg.Resources = s.Resources
	m.cfg.SliceMemoryMax = s.SliceMax
	return m.SaveConfig()
}

// HostPolicy is the host-wide policy (beyond capacity) surfaced by the TUI Settings
// panel and the `srm config` CLI: rootless DinD + its BuildKit image override, per-org
// isolation, and the ProtectProc hardening toggle. These were config-file-only before.
type HostPolicy struct {
	RootlessDinD  bool
	BuildkitImage string
	PerOrgUsers   bool
	ProtectProc   bool
}

// HostPolicy returns the current host-wide policy toggles.
func (m *Manager) HostPolicy() HostPolicy {
	c := m.currentConfig()
	return HostPolicy{
		RootlessDinD:  c.Docker.RootlessDinD,
		BuildkitImage: c.Docker.BuildkitImage,
		PerOrgUsers:   c.Isolation.PerOrgUsers,
		ProtectProc:   c.Hardening.ProtectProc,
	}
}

// ApplyHostPolicy updates the host-wide policy toggles and persists them. Like
// ApplyResourceSettings the change affects SUBSEQUENT orchestrator builds; existing
// units pick up unit-level changes (ProtectProc) after `srm runners refresh` and
// ephemeral lanes after recreate. Enabling RootlessDinD still needs the host prereqs
// (`srm provision --rootless-dind`; `srm doctor` blocks until they are present). The
// config is swapped under the lock (never mutated in place) so a concurrent reader
// can't observe a torn value.
func (m *Manager) ApplyHostPolicy(p HostPolicy) error {
	m.mu.Lock()
	next := *m.cfg
	next.Docker.RootlessDinD = p.RootlessDinD
	next.Docker.BuildkitImage = strings.TrimSpace(p.BuildkitImage)
	next.Isolation.PerOrgUsers = p.PerOrgUsers
	next.Hardening.ProtectProc = p.ProtectProc
	m.cfg = &next
	m.mu.Unlock()
	return m.SaveConfig()
}

// ApplyHostManifest replaces the host dependency manifest (apt packages, setup
// scripts, tool-cache seeds, persistent cache paths) and persists it. The manifest
// is applied by `srm provision` (the TUI Provision op / `srm provision`), host-once
// and never per job, so the change takes effect on the next provision. The config is
// swapped under the lock (never mutated in place) so a concurrent reader can't
// observe a torn value, matching ApplyHostPolicy.
func (m *Manager) ApplyHostManifest(man core.DependencyManifest) error {
	m.mu.Lock()
	next := *m.cfg
	next.Host = man
	m.cfg = &next
	m.mu.Unlock()
	return m.SaveConfig()
}
