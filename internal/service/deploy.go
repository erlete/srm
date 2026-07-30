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
	GroupID    int64    // runner group id; used only when Group (name) is empty (e.g. from a profile)
	Profile    string   // named org create-preset to seed empty fields from (see applyProfileDefaults)
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

// agentDownload returns the x64 runner archive GitHub currently publishes for the org
// (URL + publisher SHA256). The OS is fixed per build via runner.AssetOS ("linux" /
// "win"), matching GitHub's runner-application API OS string.
func (m *Manager) agentDownload(ctx context.Context, org string) (runner.Download, error) {
	c, err := m.client(ctx, org)
	if err != nil {
		return runner.Download{}, err
	}
	ds, err := c.ListRunnerDownloads(ctx, org)
	if err != nil {
		return runner.Download{}, err
	}
	for _, d := range ds {
		if d.OS == runner.AssetOS && d.Arch == "x64" {
			return runner.Download{URL: d.URL, SHA256: d.SHA256}, nil
		}
	}
	return runner.Download{}, fmt.Errorf("GitHub offered no %s/x64 runner download for %s", runner.AssetOS, org)
}

// resolveDownload picks the agent tarball to install for an org, honoring an
// optional explicit target version (from `--to-version` or runnerVersionPin).
//
// With no target (the create-time and unpinned-upgrade default) it returns the
// linux/x64 download GitHub currently offers - byte-identical to the historical
// behavior, and always carrying a publisher SHA256.
//
// With a target it INSISTS on a verified checksum. GitHub's runner-application
// API only advertises the single current version, so a checksum is resolvable
// exactly when the target equals what GitHub serves today. For any other version
// (an older or future pin) we could synthesize the download URL but have no
// trustworthy SHA256, so we REFUSE rather than install an unverifiable agent.
// That is the safe failure: a pin is a deliberate act, and a deliberate act that
// cannot be made safe should stop, not silently downgrade integrity.
func (m *Manager) resolveDownload(ctx context.Context, org, target string) (runner.Download, error) {
	dl, err := m.agentDownload(ctx, org)
	if err != nil {
		return runner.Download{}, err
	}
	if target == "" {
		return dl, nil
	}
	current := runner.VersionFromURL(dl.URL)
	if target == current {
		return dl, nil
	}
	return runner.Download{}, fmt.Errorf(
		"cannot install runner agent %s with a verified checksum: GitHub currently publishes %s, and its API exposes a checksum only for that version - refusing to install an unverifiable agent (use --to-version %s, or omit the pin to track the published version)",
		target, current, current)
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
		User:          m.cfg.RunnerUserFor(org),
		CacheRoot:     m.cfg.CacheRootFor(org), // "" in single-user mode → NewUbuntu defaults
		ToolCache:     m.cfg.ToolCacheFor(org), // "" in single-user mode → NewUbuntu defaults
		Resources:     m.cfg.ResourcesFor(org),
		Org:           org,
		Isolated:      m.cfg.Isolation.PerOrgUsers,
		ProtectProc:   m.cfg.Hardening.ProtectProc,
		DinD:          m.cfg.Docker.RootlessDinD,
		BuildkitImage: m.cfg.Docker.BuildkitImage,
	}
	// Auto-capacity mode bounds ALL runners' aggregate memory via a shared slice
	// (machine-relative, so it scales with the host). Left empty otherwise, keeping
	// the drop-in byte-identical to the historical output.
	if m.cfg.ResourceMode == config.ResourceModeAuto {
		opts.Slice = runner.AggregateSlice
		opts.SliceMemoryMax = m.cfg.SliceMemoryMaxOrDefault()
	}
	// An ephemeral slot unit's ExecStart calls back into this srm binary; bake its
	// real path (falls back to DefaultSelfExe if undeterminable) and the config file
	// this srm loaded from, so the detached cycle loads the same config the lane was
	// created with (mirrors the Windows supervisor's --config bake).
	if exe, err := os.Executable(); err == nil {
		opts.SelfExe = exe
	}
	opts.ConfigPath = m.cfgPath
	return runner.NewForHost(m.installRootFor(org), opts)
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
	// A named profile seeds empty fields (labels/group) first, so the org-default
	// fallbacks below only fill what neither the caller nor the profile set.
	if err := m.applyProfileDefaults(org, &spec, false); err != nil {
		return nil, err
	}
	// A profile (or caller) may pass a group by ID; the create path takes a NAME, so
	// resolve it. Skipped under dry-run to stay API-free.
	if spec.Group == "" && spec.GroupID > 0 && !m.cfg.DryRun {
		spec.Group = m.groupNameByID(ctx, org, spec.GroupID)
	}
	// Fall back to the org's configured runner defaults when neither the caller nor a
	// profile supplied them: labels (OrgConfig.DefaultLabels; see defaultLabels) and
	// group. The persistent path previously IGNORED DefaultGroupID (ephemeral already
	// honored it) - resolve the configured default group's NAME here (the create path
	// takes a name; JIT/ephemeral takes an id). Skipped under dry-run to stay API-free.
	if len(spec.Labels) == 0 {
		spec.Labels = m.defaultLabels(org)
	}
	if spec.Group == "" && !m.cfg.DryRun {
		spec.Group = m.groupNameByID(ctx, org, m.defaultGroupID(org))
	}

	if spec.Group != "" {
		if _, err := m.GetOrCreateGroup(ctx, org, spec.Group); err != nil {
			return nil, fmt.Errorf("ensure group %q: %w", spec.Group, err)
		}
	}

	dl, err := m.agentDownload(ctx, org)
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
		// Annotate the manifest with the installed agent version and the host-bound
		// GitHub id read from the just-written .runner file. Advisory and best-effort:
		// a record failure must never fail a create that already succeeded.
		id, _ := orch.AgentID(org, name)
		_ = m.recordCreated(RunnerRecord{
			Kind:            KindPersistent,
			Org:             org,
			Name:            name,
			GitHubRunnerID:  id,
			AgentVersion:    runner.VersionFromURL(dl.URL),
			TemplateVersion: runner.CurrentDropInVersion,
		})
	}
	return names, nil
}

// groupNameByID resolves a runner-group id to its name for this org, best-effort:
// it returns "" for the Default group (id <= DefaultGroupID, which the create path
// represents as an empty group name) and "" on any lookup failure (the caller then
// lands the runner in the Default group). The persistent create path takes a group
// NAME, whereas JIT/ephemeral takes the id - this bridges an id-valued config
// (OrgConfig.DefaultGroupID) onto the name the create path needs.
func (m *Manager) groupNameByID(ctx context.Context, org string, id int64) string {
	if id <= DefaultGroupID {
		return ""
	}
	gs, err := m.ListGroups(ctx, org)
	if err != nil {
		return ""
	}
	for _, g := range gs {
		if g.ID == id {
			return g.Name
		}
	}
	return ""
}

// RecreateRunner tears a persistent runner down and stands it back up on THIS host
// with the SAME name, custom labels, and group (BACKLOG #2). It is the clean-slate
// remedy for an unhealthy or stuck runner whose drop-in refresh is not enough. The
// recreated runner gets a fresh GitHub registration (new id) under the same name.
// groupID is resolved to the group NAME the create path expects (0 / Default -> the
// default group). Must run as root. Honors dry-run.
func (m *Manager) RecreateRunner(ctx context.Context, org, name string, labels []string, groupID int64) error {
	org, err := m.requireOrg(org)
	if err != nil {
		return err
	}
	// Resolve the group name before teardown (CreateRunner takes a group NAME). A
	// lookup failure is non-fatal: recreate then lands the runner in the default group.
	group := m.groupNameByID(ctx, org, groupID)
	if err := m.DestroyRunner(ctx, org, name); err != nil {
		return fmt.Errorf("destroy %s: %w", name, err)
	}
	if m.cfg.DryRun {
		return nil
	}
	dl, err := m.agentDownload(ctx, org)
	if err != nil {
		return err
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return err
	}
	tok, err := c.CreateRegistrationToken(ctx, org)
	if err != nil {
		return fmt.Errorf("mint registration token: %w", err)
	}
	orch := m.orchestratorFor(org)
	if err := orch.EnsureBase(ctx); err != nil {
		return fmt.Errorf("provision host base: %w", err)
	}
	rspec := runner.RunnerSpec{Name: name, Org: org, URL: "https://github.com/" + org, Labels: labels, Group: group}
	if err := orch.CreateRunner(ctx, rspec, dl, tok.Value); err != nil {
		return fmt.Errorf("recreate %s: %w", name, err)
	}
	id, _ := orch.AgentID(org, name)
	_ = m.recordCreated(RunnerRecord{
		Kind: KindPersistent, Org: org, Name: name, GitHubRunnerID: id,
		AgentVersion: runner.VersionFromURL(dl.URL), TemplateVersion: runner.CurrentDropInVersion,
	})
	return nil
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
// stage the isolation migration one org at a time. only (when non-empty) further
// restricts to a specific runner selection (keys via RunnerRef).
func (m *Manager) RefreshLocalUnits(ctx context.Context, orgFilter string, only map[string]bool) ([]RefreshResult, map[string]error) {
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
		if len(only) > 0 && !only[RunnerRef(row.Org, row.Runner.Name)] {
			continue // a bounded selection is active and this runner is not in it
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

// PruneDepCacheRun is PruneDepCache with per-call dry-run threading: dryRun=true
// reports what would be evicted (for a preview) without deleting; dryRun=false
// performs the eviction. Same save/restore approach as ReconcileFix (decision #10).
func (m *Manager) PruneDepCacheRun(ctx context.Context, maxAge time.Duration, dryRun bool) (runner.PruneStats, error) {
	defer m.withDryRun(dryRun)()
	return m.PruneDepCache(ctx, maxAge)
}

// CacheRetentionDaysOrDefault is the configured dep-cache age cutoff, or 30 days.
func (m *Manager) CacheRetentionDaysOrDefault() int {
	if m.cfg.CacheRetentionDays > 0 {
		return m.cfg.CacheRetentionDays
	}
	return 30
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
	// The on-disk tree is gone, so drop the manifest entry too (advisory; a failure
	// here at worst leaves a stale entry that the next reconcile/list ignores).
	_ = m.forget(KindPersistent, org, name)

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
