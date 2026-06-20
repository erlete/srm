package service

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/runner"
)

// DeploySpec parameterizes creating persistent runners on THIS host.
type DeploySpec struct {
	Org        string
	Count      int
	NamePrefix string
	Labels     []string // custom labels (tags)
	Group      string   // runner group name; created if missing
}

// CreateGroup creates a runner group (honors dry-run).
func (m *Manager) CreateGroup(ctx context.Context, org, name, visibility string) (core.Group, error) {
	org, err := m.requireOrg(org)
	if err != nil {
		return core.Group{}, err
	}
	if visibility == "" {
		visibility = "all"
	}
	if m.cfg.DryRun {
		return core.Group{Name: name, Visibility: visibility}, nil
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return core.Group{}, err
	}
	return c.CreateGroup(ctx, org, name, visibility)
}

// GetOrCreateGroup returns the named group, creating it (visibility "all") if absent.
func (m *Manager) GetOrCreateGroup(ctx context.Context, org, name string) (core.Group, error) {
	org, err := m.requireOrg(org)
	if err != nil {
		return core.Group{}, err
	}
	groups, err := m.ListGroups(ctx, org)
	if err != nil {
		return core.Group{}, err
	}
	for _, g := range groups {
		if g.Name == name {
			return g, nil
		}
	}
	return m.CreateGroup(ctx, org, name, "all")
}

func (m *Manager) linuxDownload(ctx context.Context, org string) (runner.Download, error) {
	c, err := m.client(ctx, org)
	if err != nil {
		return runner.Download{}, err
	}
	ds, err := c.ListRunnerDownloads(ctx, org)
	if err != nil {
		return runner.Download{}, err
	}
	for _, d := range ds {
		if d.OS == "linux" && d.Arch == "x64" {
			return runner.Download{URL: d.URL, SHA256: d.SHA256}, nil
		}
	}
	return runner.Download{}, fmt.Errorf("GitHub offered no linux/x64 runner download for %s", org)
}

// installRootFor resolves the host install root for an org: its configured
// installRoot, else the package default. An unknown/empty org also yields the
// default, which is what host-wide operations (e.g. cache prune) want.
func (m *Manager) installRootFor(org string) string {
	if oc, ok := m.cfg.Org(org); ok && oc.InstallRoot != "" {
		return oc.InstallRoot
	}
	return config.DefaultInstallRoot
}

// orchestratorFor builds the host orchestrator for an org. It is the single seam
// that maps configuration onto a runner.Options, so per-org host policy (a custom
// install root today; per-org users, caches, and resource limits later) lives in
// one place instead of being recomputed at every call site.
func (m *Manager) orchestratorFor(org string) runner.Orchestrator {
	opts := runner.Options{
		User:        m.cfg.RunnerUserFor(org),
		CacheRoot:   m.cfg.CacheRootFor(org), // "" in single-user mode → NewUbuntu defaults
		ToolCache:   m.cfg.ToolCacheFor(org), // "" in single-user mode → NewUbuntu defaults
		Resources:   m.cfg.ResourcesFor(org),
		Org:         org,
		Isolated:    m.cfg.Isolation.PerOrgUsers,
		ProtectProc: m.cfg.Hardening.ProtectProc,
	}
	// Auto-capacity mode bounds ALL runners' aggregate memory via a shared slice
	// (machine-relative, so it scales with the host). Left empty otherwise, keeping
	// the drop-in byte-identical to the historical output.
	if m.cfg.ResourceMode == config.ResourceModeAuto {
		opts.Slice = runner.AggregateSlice
		opts.SliceMemoryMax = m.cfg.SliceMemoryMaxOrDefault()
	}
	// An ephemeral slot unit's ExecStart calls back into this srm binary; bake its
	// real path (falls back to DefaultSelfExe if undeterminable).
	if exe, err := os.Executable(); err == nil {
		opts.SelfExe = exe
	}
	return runner.NewUbuntu(m.installRootFor(org), opts)
}

// CreateRunners provisions Count persistent runners on the local host. It must
// run as root on the target Ubuntu machine (it creates a user, installs systemd
// services, and runs apt). Returns the created runner names. Honors dry-run.
func (m *Manager) CreateRunners(ctx context.Context, spec DeploySpec, progress chan<- core.ProgressEvent) ([]string, error) {
	org, err := m.requireOrg(spec.Org)
	if err != nil {
		return nil, err
	}
	if spec.Count < 1 {
		spec.Count = 1
	}

	if spec.Group != "" {
		if _, err := m.GetOrCreateGroup(ctx, org, spec.Group); err != nil {
			return nil, fmt.Errorf("ensure group %q: %w", spec.Group, err)
		}
	}

	dl, err := m.linuxDownload(ctx, org)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, spec.Count)
	if m.cfg.DryRun {
		for i := 1; i <= spec.Count; i++ {
			names = append(names, fmt.Sprintf("%s-%d", spec.NamePrefix, i))
		}
		return names, nil
	}

	// One registration token covers all N runners within its ~1h validity.
	c, err := m.client(ctx, org)
	if err != nil {
		return nil, err
	}
	tok, err := c.CreateRegistrationToken(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("mint registration token: %w", err)
	}

	orch := m.orchestratorFor(org)
	if err := orch.EnsureBase(ctx); err != nil {
		return nil, fmt.Errorf("provision host base: %w", err)
	}

	url := "https://github.com/" + org
	for i := 1; i <= spec.Count; i++ {
		name := fmt.Sprintf("%s-%d", spec.NamePrefix, i)
		rspec := runner.RunnerSpec{Name: name, Org: org, URL: url, Labels: spec.Labels, Group: spec.Group}
		cerr := orch.CreateRunner(ctx, rspec, dl, tok.Value)
		if progress != nil {
			msg := "created " + name
			if cerr != nil {
				msg = fmt.Sprintf("failed %s: %v", name, cerr)
			}
			progress <- core.ProgressEvent{Index: i, Total: spec.Count, Message: msg, Err: cerr}
		}
		if cerr != nil {
			return names, fmt.Errorf("create %s: %w", name, cerr)
		}
		names = append(names, name)
	}
	return names, nil
}

// RefreshResult reports the outcome of refreshing one local runner's unit.
type RefreshResult struct {
	Org, Name string
	Refreshed bool
	Skipped   string // non-empty reason if skipped (e.g. busy)
	Err       error
}

// RefreshLocalUnits re-applies the systemd drop-in (hardening + tool-cache env +
// resource limits, and in isolated mode User=) to every srm-managed runner on
// THIS host across all orgs, and restarts each so the new directives take effect.
// Busy runners are skipped (a restart would abort their job). Must run as root.
//
// This is also the per-org-isolation migration path: when isolation is enabled,
// EnsureBase creates the org user + re-chowns its tree and RefreshUnit rewrites
// the drop-in with User=<org user>, so the restart brings the runner up under the
// new user IN PLACE - no recreate or re-registration. Returns one result per
// local runner plus any per-org list errors.
// orgFilter (when non-empty) restricts the refresh to a single org - used to
// stage the isolation migration one org at a time.
func (m *Manager) RefreshLocalUnits(ctx context.Context, orgFilter string) ([]RefreshResult, map[string]error) {
	all, errs := m.ListAllRunners(ctx)
	ensured := map[string]bool{}
	var out []RefreshResult
	for _, row := range all {
		if !row.Local {
			continue
		}
		if orgFilter != "" && row.Org != orgFilter {
			continue
		}
		res := RefreshResult{Org: row.Org, Name: row.Runner.Name}
		if row.Runner.Busy {
			res.Skipped = "busy (job running)"
			out = append(out, res)
			continue
		}
		if m.cfg.DryRun {
			res.Skipped = "dry-run"
			out = append(out, res)
			continue
		}
		orch := m.orchestratorFor(row.Org)
		// EnsureBase is host-once per shared installRoot in single-user mode, but
		// per-org when isolated (each org's user owns only its own subtree).
		ensureKey := m.installRootFor(row.Org)
		if m.cfg.Isolation.PerOrgUsers {
			ensureKey = "org:" + row.Org
		}
		if !ensured[ensureKey] {
			if err := orch.EnsureBase(ctx); err != nil {
				res.Err = fmt.Errorf("ensure base: %w", err)
				out = append(out, res)
				continue
			}
			ensured[ensureKey] = true
		}
		if err := orch.RefreshUnit(ctx, row.Org, row.Runner.Name); err != nil {
			res.Err = err
		} else {
			res.Refreshed = true
		}
		out = append(out, res)
	}
	return out, errs
}

// PruneDepCache evicts build-tool dependency-cache entries on THIS host not
// accessed within maxAge. Honors the global dry-run (report-only). Must run as
// root. In single-user mode the dep cache is one shared root; with per-org
// isolation each org has its own cache root, so prune iterates them and
// aggregates the stats (otherwise the per-org caches would grow unbounded).
func (m *Manager) PruneDepCache(ctx context.Context, maxAge time.Duration) (runner.PruneStats, error) {
	if !m.cfg.Isolation.PerOrgUsers {
		// Host-wide: one shared dep cache root (empty org → host defaults).
		return m.orchestratorFor("").PruneDepCache(ctx, maxAge, m.cfg.DryRun)
	}
	total := runner.PruneStats{Root: config.DefaultCacheRoot + "/<org>"}
	var firstErr error
	for _, org := range m.cfg.OrgNames() {
		s, err := m.orchestratorFor(org).PruneDepCache(ctx, maxAge, m.cfg.DryRun)
		total.Files += s.Files
		total.Bytes += s.Bytes
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return total, firstErr
}

// DestroyRunner removes a runner from THIS host (stops + uninstalls the service,
// deletes the tree) AND deregisters it from GitHub. Must run as root on the
// host. Honors dry-run.
func (m *Manager) DestroyRunner(ctx context.Context, org, name string) error {
	org, err := m.requireOrg(org)
	if err != nil {
		return err
	}

	orch := m.orchestratorFor(org)
	// Resolve the GitHub runner id from the agent's local .runner file BEFORE
	// teardown removes the tree. Deregistering by this host-local id (never a name
	// match across the whole org) guarantees we never delete another host's
	// same-named runner. It also means host cleanup no longer depends on GitHub
	// being reachable.
	id, idErr := orch.AgentID(org, name)

	if m.cfg.DryRun {
		return nil
	}

	// Host cleanup first (best-effort), then the authoritative API delete by id.
	hostErr := orch.RemoveRunner(ctx, name, org, "")

	if id == 0 {
		// No host-local id to deregister by - do NOT fall back to a name lookup
		// (it could resolve to another host's runner). Leave the GitHub-side
		// registration for `srm reconcile` to surface and clean as an orphan.
		if hostErr != nil {
			return hostErr
		}
		return fmt.Errorf("removed host runner %q but could not read its GitHub id (%v); deregister it via `srm reconcile`", name, idErr)
	}

	c, err := m.client(ctx, org)
	if err != nil {
		return err
	}
	if err := c.DeleteRunner(ctx, org, id); err != nil {
		return err
	}
	return hostErr
}
