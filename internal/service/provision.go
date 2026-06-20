package service

import (
	"context"
	"fmt"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/provision"
)

// ProvisionHost applies a host dependency manifest (apt packages, setup scripts,
// persistent cache paths) to THIS host. Must run as root on the target. Honors
// dry-run. With per-org isolation, apt/scripts run ONCE host-global, then the
// tool-cache seeds are warmed into EACH org's private tool cache (owned by that
// org's user) - otherwise isolated runners would point at an empty per-org cache.
func (m *Manager) ProvisionHost(ctx context.Context, man core.DependencyManifest) error {
	if m.cfg.DryRun {
		return nil
	}
	if !m.cfg.Isolation.PerOrgUsers {
		return provision.NewUbuntu(m.cfg.RunnerUser).Apply(ctx, man)
	}
	base := man
	base.ToolCacheSeeds = nil // host-global apt/scripts/cachePaths only
	if err := provision.NewUbuntu(m.cfg.RunnerUser).Apply(ctx, base); err != nil {
		return err
	}
	for _, org := range m.cfg.OrgNames() {
		r := provision.NewUbuntuFor(m.cfg.RunnerUserFor(org), m.cfg.ToolCacheFor(org))
		if err := r.Seed(ctx, man.ToolCacheSeeds); err != nil {
			return fmt.Errorf("seed %s tool cache: %w", org, err)
		}
	}
	return nil
}

// HostDrift reports manifest items not yet present on this host. With isolation,
// apt/scripts are checked once and tool-cache seeds are checked per org (a missing
// seed is reported as e.g. "toolcache:node@22 (Acme)").
func (m *Manager) HostDrift(ctx context.Context, man core.DependencyManifest) ([]string, error) {
	if !m.cfg.Isolation.PerOrgUsers {
		return provision.NewUbuntu(m.cfg.RunnerUser).Drift(ctx, man)
	}
	base := man
	base.ToolCacheSeeds = nil
	missing, err := provision.NewUbuntu(m.cfg.RunnerUser).Drift(ctx, base)
	if err != nil {
		return nil, err
	}
	for _, org := range m.cfg.OrgNames() {
		r := provision.NewUbuntuFor(m.cfg.RunnerUserFor(org), m.cfg.ToolCacheFor(org))
		perOrg, err := r.Drift(ctx, core.DependencyManifest{ToolCacheSeeds: man.ToolCacheSeeds})
		if err != nil {
			return nil, err
		}
		for _, item := range perOrg {
			missing = append(missing, item+" ("+org+")")
		}
	}
	return missing, nil
}

// HostManifest returns the configured host-once manifest.
func (m *Manager) HostManifest() core.DependencyManifest { return m.cfg.Host }
