package service

import (
	"testing"

	"github.com/erlete/srm/internal/config"
)

func TestUninstallBaseTargets(t *testing.T) {
	mk := func(isolated bool, orgs ...string) *Manager {
		cfg := &config.Config{RunnerUser: "srm"}
		cfg.Isolation.PerOrgUsers = isolated
		for _, o := range orgs {
			cfg.Orgs = append(cfg.Orgs, config.OrgConfig{Name: o})
		}
		return New(cfg, nil, "")
	}
	has := func(xs []string, want string) bool {
		for _, x := range xs {
			if x == want {
				return true
			}
		}
		return false
	}

	t.Run("single-user full purge leaves the shared tool cache", func(t *testing.T) {
		u, p := mk(false, "Acme", "Globex").uninstallBaseTargets(UninstallOpts{})
		if len(u) != 1 || !has(u, "srm") {
			t.Errorf("users = %v, want [srm]", u)
		}
		if !has(p, config.DefaultCacheRoot) || !has(p, config.DefaultInstallRoot) || !has(p, "/var/lib/srm") {
			t.Errorf("paths missing srm-private roots: %v", p)
		}
		if has(p, config.DefaultToolCacheRoot) {
			t.Errorf("must NOT remove shared tool cache %s: %v", config.DefaultToolCacheRoot, p)
		}
	})

	t.Run("isolated full purge removes per-org users and caches", func(t *testing.T) {
		u, p := mk(true, "Acme", "Globex").uninstallBaseTargets(UninstallOpts{})
		if !has(u, "srm-acme") || !has(u, "srm-globex") {
			t.Errorf("users = %v, want srm-acme + srm-globex", u)
		}
		if !has(p, config.DefaultToolCacheRoot+"/Acme") || !has(p, config.DefaultCacheRoot+"/Globex") {
			t.Errorf("paths missing per-org caches: %v", p)
		}
	})

	t.Run("single-org isolated touches only that org, never shared infra", func(t *testing.T) {
		u, p := mk(true, "Acme", "Globex").uninstallBaseTargets(UninstallOpts{Org: "Acme"})
		if len(u) != 1 || !has(u, "srm-acme") {
			t.Errorf("users = %v, want [srm-acme]", u)
		}
		if has(u, "srm-globex") {
			t.Error("must not touch another org's user")
		}
		if has(p, config.DefaultCacheRoot) || has(p, config.DefaultInstallRoot) {
			t.Errorf("must not remove shared roots: %v", p)
		}
		if !has(p, "/var/lib/srm/ephemeral/Acme") {
			t.Errorf("missing /var/lib/srm/ephemeral/Acme: %v", p)
		}
	})

	t.Run("single-org single-user removes no user and no shared roots", func(t *testing.T) {
		u, p := mk(false, "Acme").uninstallBaseTargets(UninstallOpts{Org: "Acme"})
		if len(u) != 0 {
			t.Errorf("must remove no user (shared), got %v", u)
		}
		if has(p, config.DefaultCacheRoot) || has(p, "/var/lib/srm") {
			t.Errorf("must not remove shared roots: %v", p)
		}
	})
}
