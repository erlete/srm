package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/erlete/srm/internal/config"
	ghub "github.com/erlete/srm/internal/github"
	"github.com/erlete/srm/internal/runner"
)

// Drift classes for a runner as seen by reconcile.
const (
	ClassHealthy      = "healthy"       // local, registered, conformant, active+online
	ClassOrphanUnit   = "orphan-unit"   // host unit/tree exists, NOT on GitHub → host teardown
	ClassStaleDropIn  = "stale-dropin"  // drop-in != expected (cache env / limits / user / older template) → refresh
	ClassDropInNewer  = "dropin-newer"  // on-disk drop-in is a NEWER template generation than this srm → authoritative-skip (update the binary)
	ClassStuck        = "stuck"         // local but dead or offline → restart
	ClassLegacyFlat   = "legacy-flat"   // tree at the pre-namespacing flat path → report
	ClassOrphanGitHub = "orphan-github" // on GitHub, no managed unit here → report only
	ClassUnknown      = "unknown"       // GitHub list failed for the org → cannot classify, no fix

	// Ephemeral slot lanes are a SEPARATE family judged by host health alone - never
	// by GitHub registration presence, which churns every job. A healthy lane is
	// normal (not drift); a down/crash-looping lane is reported.
	ClassEphemeralSlot  = "ephemeral"       // active lane, not crash-looping → healthy
	ClassEphemeralStuck = "ephemeral-stuck" // lane inactive or crash-looping → report
	ClassEphemeralNewer = "ephemeral-newer" // on-disk lane unit is a NEWER template generation than this srm → authoritative-skip
)

// EphemeralRestartThreshold is the systemd NRestarts count above which an
// ephemeral slot is treated as crash-looping rather than healthily churning jobs.
const EphemeralRestartThreshold = 5

// RunnerState is reconcile's verdict for one runner.
type RunnerState struct {
	Org, Name string
	Class     string
	Detail    string
	Local     bool   // a unit/tree for it exists on THIS host
	Active    bool   // its unit is running
	User      string // effective unit User=
	OnGitHub  bool
	Online    bool
	Busy      bool
	MemPeak   int64  // cgroup bytes, -1 unknown
	MemMax    int64  // cgroup bytes, -1 unlimited
	MemCur    int64  // cgroup live bytes (MemoryCurrent), -1 unknown
	OOMKills  int64  // cgroup memory.events oom_kill count, -1 unknown (0 = none)
	Fix       string // repair applied or planned ("" = none)
	FixErr    error
}

// CacheSize is a labeled directory size for the host-health report.
type CacheSize struct {
	Label string
	Bytes int64
}

// ReconcileReport is the full host+fleet state assessment.
type ReconcileReport struct {
	Runners  []RunnerState
	Disks    []runner.DiskStat
	Caches   []CacheSize
	ListErrs map[string]error
	Applied  bool          // true if --fix actually ran (not dry-run/report-only)
	Reaped   []RunnerState // offline ephemeral ghosts handled by --reap-ephemeral

	// SliceCurrent/SliceMax are the live usage and hard cap of the srm aggregate
	// slice (cgroup v2), -1 when no auto-capacity slice exists. This is the box-wide
	// ceiling for ALL runners combined - the early-warning number for host OOM.
	SliceCurrent int64
	SliceMax     int64
}

// Reconcile assesses host vs GitHub state, classifies drift, gathers host health,
// and (when fix is set and not dry-run) repairs host-side drift. It NEVER deletes
// anything on GitHub EXCEPT when reapEphemeral is set, the single gated exception:
// offline, non-busy ephemeral JIT registrations (ghosts left by a crashed cycle)
// are deregistered. orphan-github is always report-only (a runner with no unit
// here is likely another host's). orgFilter (non-empty) scopes to one org. Must
// run as root on the host.
func (m *Manager) Reconcile(ctx context.Context, fix, reapEphemeral bool, orgFilter string) (ReconcileReport, error) {
	rep := ReconcileReport{}

	// GitHub side, keyed by (org, name).
	all, errs := m.ListAllRunners(ctx)
	rep.ListErrs = errs
	type key struct{ org, name string }
	gh := make(map[key]RunnerWithOrg, len(all))
	for _, row := range all {
		if orgFilter != "" && row.Org != orgFilter {
			continue
		}
		gh[key{row.Org, row.Runner.Name}] = row
	}
	// An org whose GitHub list FAILED is not in errs==nil; for such orgs we must
	// NOT treat "absent from gh" as "deregistered" - that would mass-misclassify
	// healthy local units as orphan-unit and (with --fix) delete them. Track which
	// orgs we can actually trust the GitHub view for.
	listedOK := make(map[string]bool, len(m.cfg.OrgNames()))
	for _, name := range m.cfg.OrgNames() {
		if _, failed := errs[name]; !failed {
			listedOK[name] = true
		}
	}

	// Host side: enumerate every managed unit (orphans included). A failure here
	// makes the whole report meaningless (every GitHub runner would look orphaned),
	// so fail loudly rather than emit a misleading assessment.
	host := m.orchestratorFor("")
	units, err := host.ListUnits(ctx)
	if err != nil {
		return rep, fmt.Errorf("enumerate host runner units (run as root on the host?): %w", err)
	}
	orgsByLen := append([]string{}, m.cfg.OrgNames()...)
	sort.Slice(orgsByLen, func(i, j int) bool { return len(orgsByLen[i]) > len(orgsByLen[j]) })

	seen := make(map[key]bool)
	for _, svc := range units {
		org, name, ok := parseUnitName(svc, orgsByLen)
		if !ok || (orgFilter != "" && org != orgFilter) {
			continue
		}
		k := key{org, name}
		seen[k] = true
		insp := m.orchestratorFor(org).Inspect(ctx, org, name)
		st := RunnerState{
			Org: org, Name: name, Local: true,
			Active: insp.Active, User: insp.User,
			MemPeak: insp.MemPeakBytes, MemMax: insp.MemMaxBytes,
			MemCur: insp.MemCurBytes, OOMKills: insp.OOMKills,
		}
		row, on := gh[k]
		st.OnGitHub = on
		if on {
			st.Online = row.Runner.Status == "online"
			st.Busy = row.Runner.Busy
		}
		st.Class, st.Detail = classifyPersistent(insp, on, listedOK[org], st.Online)
		rep.Runners = append(rep.Runners, st)
	}

	// Ephemeral slot lanes: a SEPARATE inventory, classified by HOST HEALTH ONLY.
	// They are deliberately never joined to the GitHub runner list - a slot mints a
	// fresh single-use JIT registration each job, so a unit↔registration join is
	// impossible and would mass-misclassify a between-jobs lane as an orphan.
	eph, err := host.ListEphemeralUnits(ctx)
	if err != nil {
		return rep, fmt.Errorf("enumerate host ephemeral slot units: %w", err)
	}
	for _, svc := range eph {
		org, slot, ok := parseEphemeralUnitName(svc, orgsByLen)
		if !ok || (orgFilter != "" && org != orgFilter) {
			continue
		}
		insp := m.orchestratorFor(org).InspectEphemeral(ctx, org, slot)
		st := RunnerState{
			Org: org, Name: slot, Local: true, Active: insp.Active,
			MemPeak: insp.MemPeakBytes, MemMax: insp.MemMaxBytes,
			MemCur: insp.MemCurBytes, OOMKills: insp.OOMKills,
		}
		st.Class, st.Detail = classifyEphemeral(insp)
		rep.Runners = append(rep.Runners, st)
	}

	// GitHub runners with no managed unit here. Online ones are healthy on another
	// host (skip - not this host's drift). Offline ones are reported as orphans.
	for _, row := range all {
		if orgFilter != "" && row.Org != orgFilter {
			continue
		}
		k := key{row.Org, row.Runner.Name}
		if seen[k] {
			continue
		}
		// An ephemeral slot's JIT registration is owned by its lane unit, not a
		// stray runner - never class it orphan-github. With --reap-ephemeral, an
		// OFFLINE non-busy one is a ghost (a healthy slot's runner is online or
		// busy, and a finished one auto-deregisters), so deregister it: the single
		// gated exception to "reconcile never deletes on GitHub".
		if runner.IsEphemeralRunnerName(row.Runner.Name) {
			if reapEphemeral && row.Runner.Status != "online" && !row.Runner.Busy {
				m.maybeReapGhost(ctx, &rep, row)
			}
			continue
		}
		online := row.Runner.Status == "online"
		if online && !row.Local {
			continue // another host's healthy runner
		}
		rep.Runners = append(rep.Runners, RunnerState{
			Org: row.Org, Name: row.Runner.Name, OnGitHub: true,
			Online: online, Busy: row.Runner.Busy, Local: row.Local,
			Class:  ClassOrphanGitHub,
			Detail: "registered on GitHub, no managed unit on this host",
			// No host inspection for these - keep the -1 "unknown" sentinel rather
			// than 0 ("measured zero"), consistent with the inspected builders.
			MemPeak: -1, MemMax: -1, MemCur: -1, OOMKills: -1,
		})
	}

	sort.Slice(rep.Runners, func(i, j int) bool {
		if rep.Runners[i].Org != rep.Runners[j].Org {
			return rep.Runners[i].Org < rep.Runners[j].Org
		}
		return rep.Runners[i].Name < rep.Runners[j].Name
	})

	// Host health.
	rep.Disks = host.HostDisk(ctx, []string{config.DefaultInstallRoot, config.DefaultCacheRoot, config.DefaultToolCacheRoot})
	rep.Caches = m.cacheSizes(ctx, host)
	rep.SliceCurrent, rep.SliceMax = host.SliceUsage(ctx)

	// Repair (host-side only; honors dry-run).
	if fix {
		apply := !m.cfg.DryRun
		rep.Applied = apply
		for i := range rep.Runners {
			m.repairRunner(ctx, &rep.Runners[i], apply)
		}
	}
	return rep, nil
}

// maybeReapGhost reaps an offline ephemeral registration ONLY if it is genuinely
// abandoned: this host owns the slot lane, and the registration is not the lane's
// current in-flight id. This excludes the mint→connect window (where a live runner
// is briefly offline) and another host's registrations - a false positive here
// would kill an in-flight CI job, since this is the one gated GitHub-delete path.
func (m *Manager) maybeReapGhost(ctx context.Context, rep *ReconcileReport, row RunnerWithOrg) {
	slot, ok := runner.EphemeralSlotFromName(row.Runner.Name, row.Org)
	if !ok {
		return // malformed name - never touch
	}
	orch := m.orchestratorFor(row.Org)
	insp := orch.InspectEphemeral(ctx, row.Org, slot)
	if !insp.UnitExists {
		return // this host doesn't own the slot lane (belongs to another host)
	}
	// While the lane is actively running a cycle, don't reap its current/just-minted
	// registration: skip the recorded in-flight id, and skip entirely mid-mint
	// (PendingJIT not yet written) - we can't tell the live id from a ghost then.
	if insp.Active {
		if pending := orch.PendingJIT(row.Org, slot); pending == 0 || row.Runner.ID == pending {
			return
		}
	}
	m.reapGhost(ctx, rep, row)
}

// reapGhost deregisters an offline ephemeral JIT registration (a ghost from a
// crashed cycle) and records the outcome. Honors dry-run. This is the ONLY path
// in reconcile that deletes a GitHub runner, gated behind --reap-ephemeral; callers
// must pre-screen via maybeReapGhost.
func (m *Manager) reapGhost(ctx context.Context, rep *ReconcileReport, row RunnerWithOrg) {
	rs := RunnerState{
		Org: row.Org, Name: row.Runner.Name, OnGitHub: true,
		Class: ClassEphemeralStuck, Detail: "offline ephemeral registration (ghost)",
		MemPeak: -1, MemMax: -1, MemCur: -1, OOMKills: -1, // not inspected → unknown, not 0
	}
	switch {
	case m.cfg.DryRun:
		rs.Fix = "would reap (deregister)"
	default:
		// 404 = a concurrent cycle already reaped it; treat as success, not failure.
		if c, err := m.client(ctx, row.Org); err != nil {
			rs.FixErr = err
		} else if err := c.DeleteRunner(ctx, row.Org, row.Runner.ID); err != nil && !ghub.IsNotFound(err) {
			rs.FixErr = err
		} else {
			rs.Fix = "reaped (deregistered)"
		}
	}
	rep.Reaped = append(rep.Reaped, rs)
}

// classifyPersistent maps a persistent runner's inspected state to a drift class.
// Pure and ORDER-SENSITIVE, kept standalone so the ordering is unit-tested: a
// newer-generation drop-in also fails the conformance check (DropInOK=false), so
// DropInNewer MUST be evaluated before !DropInOK or we would refresh a newer host's
// drop-in back down (the ping-pong the marker exists to prevent). online is the
// runner's GitHub online state (meaningful only when onGitHub).
func classifyPersistent(insp runner.Inspection, onGitHub, orgListedOK, online bool) (class, detail string) {
	switch {
	case !onGitHub && !orgListedOK:
		return ClassUnknown, "GitHub list failed for this org - not classified (no fix)"
	case !onGitHub:
		return ClassOrphanUnit, "host unit not registered on GitHub"
	case insp.HasFlatTree && !insp.HasTree:
		return ClassLegacyFlat, "install tree at legacy flat path"
	case insp.DropInNewer:
		return ClassDropInNewer, fmt.Sprintf("drop-in template v%d is newer than this srm (v%d) - update the binary", insp.DropInVer, runner.CurrentDropInVersion)
	case !insp.DropInOK:
		return ClassStaleDropIn, "drop-in differs from expected (cache env / limits / user / older template)"
	case !insp.Active:
		return ClassStuck, "unit not active"
	case !online:
		return ClassStuck, "active locally but offline on GitHub"
	default:
		return ClassHealthy, ""
	}
}

// classifyEphemeral maps an ephemeral slot's host-only inspected state to a class.
// Pure and order-sensitive (unit-tested): UnitNewer is evaluated before the
// stuck/drift cases so an older binary authoritative-skips a newer host's lane unit
// rather than reporting it as drift-to-recreate (mirrors classifyPersistent).
func classifyEphemeral(insp runner.EphemeralInspection) (class, detail string) {
	switch {
	case insp.Active && insp.Restarts < EphemeralRestartThreshold && insp.UnitOK:
		return ClassEphemeralSlot, "ephemeral slot healthy"
	case insp.UnitNewer:
		return ClassEphemeralNewer, fmt.Sprintf("ephemeral unit template v%d is newer than this srm (v%d) - update the binary", insp.UnitVer, runner.CurrentEphemeralVersion)
	case !insp.Active:
		return ClassEphemeralStuck, "ephemeral slot inactive"
	case !insp.UnitOK:
		return ClassEphemeralStuck, "ephemeral unit drifted from expected (recreate to apply)"
	default:
		return ClassEphemeralStuck, fmt.Sprintf("ephemeral slot crash-looping (%d restarts)", insp.Restarts)
	}
}

// repairRunner applies (or, when apply=false, plans) the host-side fix for a
// runner's drift class. GitHub-side state is never touched.
func (m *Manager) repairRunner(ctx context.Context, st *RunnerState, apply bool) {
	orch := m.orchestratorFor(st.Org)
	switch st.Class {
	case ClassOrphanUnit:
		// Defense-in-depth: an orphan we believe is unregistered should never be
		// removed while a job is running on it.
		if st.Busy {
			st.Fix = "skipped (busy)"
			return
		}
		if !apply {
			st.Fix = "would remove orphan unit"
			return
		}
		if err := orch.RemoveRunner(ctx, st.Name, st.Org, ""); err != nil {
			st.FixErr = err
		} else {
			st.Fix = "removed orphan unit"
		}
	case ClassStaleDropIn, ClassStuck:
		if st.Busy {
			st.Fix = "skipped (busy)"
			return
		}
		if !apply {
			st.Fix = "would refresh"
			return
		}
		if err := orch.RefreshUnit(ctx, st.Org, st.Name); err != nil {
			st.FixErr = err
		} else {
			st.Fix = "refreshed"
		}
	case ClassDropInNewer, ClassEphemeralNewer:
		// Authoritative-skip: this binary is OLDER than the on-disk template, so
		// refreshing would downgrade a newer host's unit. Never touch it; the fix is
		// to update this srm binary (surfaced in the report and by `srm doctor`).
		st.Fix = "skipped (on-disk template newer than this srm; update the binary)"
		// legacy-flat and orphan-github are report-only (no safe automatic host fix).
	}
}

// cacheSizes measures the host caches for the health report (per-org dep caches
// when isolation is on, else the shared roots).
func (m *Manager) cacheSizes(ctx context.Context, host runner.Orchestrator) []CacheSize {
	var out []CacheSize
	out = append(out, CacheSize{"tool cache " + config.DefaultToolCacheRoot, host.DirSize(ctx, config.DefaultToolCacheRoot)})
	if m.cfg.Isolation.PerOrgUsers {
		for _, org := range m.cfg.OrgNames() {
			out = append(out, CacheSize{"dep cache " + config.DefaultCacheRoot + "/" + org, host.DirSize(ctx, config.DefaultCacheRoot+"/"+org)})
		}
	} else {
		out = append(out, CacheSize{"dep cache " + config.DefaultCacheRoot, host.DirSize(ctx, config.DefaultCacheRoot)})
	}
	return out
}

// parseUnitName splits a systemd unit base name (actions.runner.<org>.<name>.service)
// into org and name by matching a configured org as the prefix. orgs must be
// sorted longest-first so a longer org name wins over a shorter prefix of it.
func parseUnitName(svc string, orgs []string) (org, name string, ok bool) {
	mid := strings.TrimSuffix(strings.TrimPrefix(svc, "actions.runner."), ".service")
	if mid == svc { // prefix/suffix not present
		return "", "", false
	}
	for _, o := range orgs {
		if strings.HasPrefix(mid, o+".") {
			return o, mid[len(o)+1:], true
		}
	}
	return "", "", false
}

// parseEphemeralUnitName splits an ephemeral slot unit base name
// (actions.ephemeral.<org>.<slot>.service) into org and slot, matching a
// configured org as the prefix. orgs must be sorted longest-first so a longer org
// name wins over a shorter prefix of it (mirrors parseUnitName).
func parseEphemeralUnitName(svc string, orgs []string) (org, slot string, ok bool) {
	mid := strings.TrimSuffix(strings.TrimPrefix(svc, runner.EphemeralUnitPrefix), ".service")
	if mid == svc { // prefix/suffix not present
		return "", "", false
	}
	for _, o := range orgs {
		if strings.HasPrefix(mid, o+".") {
			return o, mid[len(o)+1:], true
		}
	}
	return "", "", false
}
