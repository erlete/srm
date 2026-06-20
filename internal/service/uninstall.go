package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/runner"
)

// UninstallOpts controls a host uninstall. The zero value (Org "", all Keep*
// false) is a full host purge that LEAVES /etc/srm in place - secrets are removed
// only with Purge.
type UninstallOpts struct {
	Org        string // "" = full host purge across every configured org
	Force      bool   // proceed despite a busy runner / mid-cycle ephemeral lane
	KeepConfig bool   // preserve /etc/srm even under Purge
	KeepBinary bool   // preserve the srm binary
	KeepGitHub bool   // host-side teardown only; make no GitHub calls
	Purge      bool   // also remove /etc/srm (config + keys) after an automatic backup
	BackupDir  string // where the pre-purge config backup is written (default /var/backups)
}

// UninstallReport is what an uninstall did, or (in dry-run) would do.
type UninstallReport struct {
	DryRun     bool
	Persistent []string // "org/name" runners removed
	Ephemeral  []string // "org/slot" lanes removed
	Users      []string // service users removed
	Paths      []string // host paths removed
	Skipped    []string // foreign units and anything intentionally left untouched
	Errors     []string // non-fatal per-artifact failures (host stays maximally cleaned)
	BackupPath string   // pre-purge config backup, if Purge ran
	ConfigDir  string   // config dir removed ("" = kept)
	Binary     string   // binary removed ("" = kept)
}

// Uninstall tears down srm-created artifacts on THIS host. It only ever removes
// things srm can attribute to itself: units are matched by the actions.runner./
// actions.ephemeral. prefixes AND a configured org (foreign units are reported and
// skipped); users are removed only when named "srm*"; caches/dirs are removed by
// exact computed path. In single-org mode (Org != "") it never touches shared
// infra (the shared user, /opt caches, the slice, /etc/srm, or the binary). The
// shared /opt/hostedtoolcache is a GitHub convention and is never removed.
//
// When cfg.DryRun is set it returns the plan without mutating and without any
// GitHub call. Must run as root on the host.
func (m *Manager) Uninstall(ctx context.Context, opts UninstallOpts) (UninstallReport, error) {
	rep := UninstallReport{DryRun: m.cfg.DryRun}

	host := m.orchestratorFor("")
	orgsByLen := append([]string{}, m.cfg.OrgNames()...)
	sort.Slice(orgsByLen, func(i, j int) bool { return len(orgsByLen[i]) > len(orgsByLen[j]) })
	inScope := func(org string) bool { return opts.Org == "" || org == opts.Org }

	units, err := host.ListUnits(ctx)
	if err != nil {
		return rep, fmt.Errorf("enumerate runner units (run as root on the host?): %w", err)
	}
	eph, err := host.ListEphemeralUnits(ctx)
	if err != nil {
		return rep, fmt.Errorf("enumerate ephemeral slot units: %w", err)
	}

	type pr struct{ org, name string }
	type es struct{ org, slot string }
	var persistent []pr
	var ephem []es
	for _, svc := range units {
		org, name, ok := parseUnitName(svc, orgsByLen)
		if !ok {
			rep.Skipped = append(rep.Skipped, "foreign unit "+svc+" (not an srm-configured org)")
			continue
		}
		if inScope(org) {
			persistent = append(persistent, pr{org, name})
		}
	}
	for _, svc := range eph {
		org, slot, ok := parseEphemeralUnitName(svc, orgsByLen)
		if !ok {
			rep.Skipped = append(rep.Skipped, "foreign ephemeral unit "+svc)
			continue
		}
		if inScope(org) {
			ephem = append(ephem, es{org, slot})
		}
	}

	for _, p := range persistent {
		rep.Persistent = append(rep.Persistent, p.org+"/"+p.name)
	}
	for _, e := range ephem {
		rep.Ephemeral = append(rep.Ephemeral, e.org+"/"+e.slot)
	}

	users, paths := m.uninstallBaseTargets(opts)
	rep.Users, rep.Paths = users, paths
	if opts.Org == "" && !m.cfg.Isolation.PerOrgUsers {
		rep.Skipped = append(rep.Skipped, "shared tool cache "+config.DefaultToolCacheRoot+" (GitHub convention - left in place)")
	}

	cfgDir := filepath.Dir(m.cfgPath)
	if opts.Purge && !opts.KeepConfig && opts.Org == "" && m.cfgPath != "" {
		rep.ConfigDir = cfgDir
	}
	if !opts.KeepBinary && opts.Org == "" {
		if exe, err := os.Executable(); err == nil {
			rep.Binary = exe
		}
	}

	if m.cfg.DryRun {
		return rep, nil // plan only - no drain check, no mutation, no GitHub
	}

	// Drain gate: never tear down work in flight unless forced.
	if !opts.Force {
		if !opts.KeepGitHub && len(persistent) > 0 {
			all, errs := m.ListAllRunners(ctx)
			busy := map[pr]bool{}
			for _, row := range all {
				if row.Runner.Busy {
					busy[pr{row.Org, row.Runner.Name}] = true
				}
			}
			for _, p := range persistent {
				if errs[p.org] != nil {
					return rep, fmt.Errorf("cannot confirm %s/%s is idle (GitHub list failed: %v) - re-run with --force or --keep-github", p.org, p.name, errs[p.org])
				}
				if busy[p] {
					return rep, fmt.Errorf("runner %s/%s is busy - drain it or re-run with --force", p.org, p.name)
				}
			}
		}
		for _, e := range ephem {
			if m.orchestratorFor(e.org).PendingJIT(e.org, e.slot) != 0 {
				return rep, fmt.Errorf("ephemeral slot %s/%s has a job in flight - wait or re-run with --force", e.org, e.slot)
			}
		}
	}

	// 1. Ephemeral lanes first (Restart=always: DestroyEphemeralSlot disables
	//    before stopping, so systemd can't respawn the cycle mid-teardown).
	for _, e := range ephem {
		var derr error
		if opts.KeepGitHub {
			derr = m.orchestratorFor(e.org).RemoveEphemeralSlot(ctx, e.org, e.slot)
		} else {
			derr = m.DestroyEphemeralSlot(ctx, e.org, e.slot)
		}
		if derr != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("ephemeral %s/%s: %v", e.org, e.slot, derr))
		}
	}
	// 2. Persistent runners (host teardown + GitHub delete by host-local id).
	for _, p := range persistent {
		var derr error
		if opts.KeepGitHub {
			derr = m.orchestratorFor(p.org).RemoveRunner(ctx, p.name, p.org, "")
		} else {
			derr = m.DestroyRunner(ctx, p.org, p.name)
		}
		if derr != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("runner %s/%s: %v", p.org, p.name, derr))
		}
	}
	// 3. Host base: users, caches, install roots, control dirs, and (full purge) slice.
	if len(users) > 0 || len(paths) > 0 || opts.Org == "" {
		if err := host.PurgeBase(ctx, runner.BasePurge{Users: users, Paths: paths, RemoveSlice: opts.Org == ""}); err != nil {
			rep.Errors = append(rep.Errors, "host base: "+err.Error())
		}
	}
	// 4. Config dir (only --purge): back up first; refuse to delete unrecoverable
	//    App keys we couldn't snapshot.
	if rep.ConfigDir != "" {
		backupDir := opts.BackupDir
		if backupDir == "" {
			backupDir = "/var/backups"
		}
		_ = os.MkdirAll(backupDir, 0o700)
		out := filepath.Join(backupDir, fmt.Sprintf("srm-config-%d.tar.gz", time.Now().Unix()))
		path, berr := BackupConfigDir(cfgDir, out)
		if berr != nil {
			return rep, fmt.Errorf("aborting config purge - backup failed (App key would be unrecoverable): %w", berr)
		}
		rep.BackupPath = path
		if err := os.RemoveAll(cfgDir); err != nil {
			rep.Errors = append(rep.Errors, "remove config dir: "+err.Error())
			rep.ConfigDir = ""
		}
	}
	// 5. Binary LAST - on Linux an unlinked running executable keeps running, so
	//    this command finishes cleanly after removing itself.
	if rep.Binary != "" {
		if err := os.Remove(rep.Binary); err != nil {
			rep.Errors = append(rep.Errors, "remove binary: "+err.Error())
			rep.Binary = ""
		}
	}
	return rep, nil
}

// uninstallBaseTargets computes the service users and host paths to remove, applying
// the safety policy: only "srm*"-named users, only srm-private paths, and (in
// single-user mode) never the shared /opt/hostedtoolcache. Per-runner trees are
// already removed by the runner/lane teardown; these cover the shared base.
func (m *Manager) uninstallBaseTargets(opts UninstallOpts) (users, paths []string) {
	cfg := m.cfg
	seenUser := map[string]bool{}
	addUser := func(name string) {
		// SAFETY: only ever remove srm's own service users, never a custom login.
		if strings.HasPrefix(name, "srm") && !seenUser[name] {
			seenUser[name] = true
			users = append(users, name)
		}
	}
	seenPath := map[string]bool{}
	addPath := func(p string) {
		if p != "" && !seenPath[p] {
			seenPath[p] = true
			paths = append(paths, p)
		}
	}

	if opts.Org == "" {
		// FULL PURGE.
		if cfg.Isolation.PerOrgUsers {
			for _, org := range cfg.OrgNames() {
				addUser(cfg.RunnerUserFor(org))
				addPath(config.DefaultCacheRoot + "/" + org)
				addPath(config.DefaultToolCacheRoot + "/" + org) // per-org tool cache is srm-private
			}
		} else {
			addUser(cfg.RunnerUser)
			addPath(config.DefaultCacheRoot) // shared dep cache is srm-private
			// /opt/hostedtoolcache is a GitHub convention shared with other tooling -
			// intentionally NOT removed (reported as skipped by the caller).
		}
		addPath(config.DefaultInstallRoot)
		for _, org := range cfg.OrgNames() {
			addPath(m.installRootFor(org)) // cover custom per-org install roots too
		}
		addPath("/var/lib/srm")
		return users, paths
	}

	// SINGLE-ORG: only this org's private artifacts; never shared infra.
	org := opts.Org
	if cfg.Isolation.PerOrgUsers {
		addUser(cfg.RunnerUserFor(org))
		addPath(config.DefaultCacheRoot + "/" + org)
		addPath(config.DefaultToolCacheRoot + "/" + org)
	}
	addPath(m.installRootFor(org) + "/" + org)
	addPath("/var/lib/srm/ephemeral/" + org)
	return users, paths
}
