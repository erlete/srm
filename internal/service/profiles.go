package service

import (
	"fmt"
	"sort"
	"strings"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
)

// Profiles are named, per-org create presets: a bundle of the registration
// parameters a `srm runners create` would otherwise take flag by flag (labels,
// group, ephemeral-or-not). They turn a repeated create incantation into one named
// choice. The container/manifest fields on core.RunnerProfile are carried as
// METADATA (editable + shown) - they are documentation of the lane's intended
// dependency + container policy, applied by the existing host-manifest + workflow
// `container:` mechanisms, NOT a per-profile provisioner (that engine does not
// exist). This is the deliberate scope: profiles drive create, they do not
// silently re-provision the host.

// Profiles returns the named create presets configured for an org (nil if none or
// the org is unknown).
func (m *Manager) Profiles(org string) []core.RunnerProfile {
	if oc, ok := m.currentConfig().Org(org); ok {
		return oc.Profiles
	}
	return nil
}

// ProfileNames returns the profile names for an org, sorted, for pickers/help.
func (m *Manager) ProfileNames(org string) []string {
	profs := m.Profiles(org)
	names := make([]string, 0, len(profs))
	for _, p := range profs {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return names
}

// Profile looks up one named profile for an org.
func (m *Manager) Profile(org, name string) (core.RunnerProfile, bool) {
	for _, p := range m.Profiles(org) {
		if p.Name == name {
			return p, true
		}
	}
	return core.RunnerProfile{}, false
}

// UpsertProfile adds or replaces (by name) a profile for an org and persists it.
// The config is swapped under the lock - the whole Orgs slice and the target org's
// Profiles slice are copied, never mutated in place - so a concurrent reader can't
// observe a torn value (matches ApplyHostPolicy / ApplyHostManifest).
func (m *Manager) UpsertProfile(org string, p core.RunnerProfile) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return fmt.Errorf("profile name is required")
	}
	return m.mutateProfiles(org, func(profs []core.RunnerProfile) ([]core.RunnerProfile, error) {
		for i := range profs {
			if profs[i].Name == p.Name {
				profs[i] = p
				return profs, nil
			}
		}
		return append(profs, p), nil
	})
}

// RemoveProfile deletes a named profile from an org and persists it. Removing an
// absent profile is an error so a typo is not a silent no-op.
func (m *Manager) RemoveProfile(org, name string) error {
	return m.mutateProfiles(org, func(profs []core.RunnerProfile) ([]core.RunnerProfile, error) {
		for i := range profs {
			if profs[i].Name == name {
				return append(profs[:i:i], profs[i+1:]...), nil
			}
		}
		return nil, fmt.Errorf("profile %q not found for org %s", name, org)
	})
}

// mutateProfiles applies fn to a COPY of an org's Profiles slice under the lock and
// persists the result. fn returns the new slice (or an error to abort with no
// change). The Orgs slice and the target org's Profiles slice are both copied so the
// swap never mutates state a concurrent reader might hold.
func (m *Manager) mutateProfiles(org string, fn func([]core.RunnerProfile) ([]core.RunnerProfile, error)) error {
	m.mu.Lock()
	idx := -1
	for i := range m.cfg.Orgs {
		if m.cfg.Orgs[i].Name == org {
			idx = i
			break
		}
	}
	if idx < 0 {
		m.mu.Unlock()
		return fmt.Errorf("org %q not configured", org)
	}
	next := *m.cfg
	next.Orgs = append([]config.OrgConfig(nil), m.cfg.Orgs...)
	profs := append([]core.RunnerProfile(nil), next.Orgs[idx].Profiles...)
	updated, err := fn(profs)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	next.Orgs[idx].Profiles = updated
	m.cfg = &next
	m.mu.Unlock()
	return m.SaveConfig()
}

// applyProfileDefaults fills a create spec from a named org profile when spec.Profile
// is set: the profile supplies labels + group where the caller left them empty
// (explicit spec values always win). The profile's Ephemeral flag is the CALLER's
// routing decision (which Create* it invokes), so it is validated against
// wantEphemeral rather than applied - a mismatch is a clear error, not a silent
// switch to the other nature.
func (m *Manager) applyProfileDefaults(org string, spec *DeploySpec, wantEphemeral bool) error {
	if spec.Profile == "" {
		return nil
	}
	p, ok := m.Profile(org, spec.Profile)
	if !ok {
		return fmt.Errorf("profile %q not found for org %s", spec.Profile, org)
	}
	if p.Ephemeral != wantEphemeral {
		nature, want := "persistent", "persistent"
		if p.Ephemeral {
			nature = "ephemeral"
		}
		if wantEphemeral {
			want = "ephemeral"
		}
		return fmt.Errorf("profile %q is %s but this is a %s create - use the matching command", spec.Profile, nature, want)
	}
	if len(spec.Labels) == 0 {
		spec.Labels = p.Labels
	}
	if spec.Group == "" && spec.GroupID == 0 && p.GroupID > 0 {
		spec.GroupID = p.GroupID
	}
	return nil
}
