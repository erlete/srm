package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"

	"github.com/erlete/srm/internal/runner"
)

// UpgradeOpts parameterizes an agent-version upgrade pass over THIS host's runners.
type UpgradeOpts struct {
	OrgFilter string          // restrict to one org ("" = every configured org)
	Only      map[string]bool // restrict to these persistent runners (keys via RunnerRef); empty = all in scope
	ToVersion string          // explicit target ("" = use RunnerVersionPin, else GitHub-current)
	DryRun    bool            // report what would change; touch nothing
	Force     bool            // re-install at the same version, and allow a downgrade
	Rollback  bool            // restore each runner to its recorded previousVersion (a deliberate downgrade)
}

// RunnerRef is the key used by UpgradeOpts.Only and RefreshLocalUnits' selection
// filter (org + name). Kept here so the TUI and the service agree on the format.
func RunnerRef(org, name string) string { return org + "\x00" + name }

// OnlyRunners builds a selection filter from a runner set (nil when empty, meaning
// "no restriction"). Used to scope upgrade/rollback/refresh to a multi-selection.
func OnlyRunners(rs []FusedRunner) map[string]bool {
	if len(rs) == 0 {
		return nil
	}
	only := make(map[string]bool, len(rs))
	for _, r := range rs {
		only[RunnerRef(r.Org, r.Runner.Name)] = true
	}
	return only
}

// UpgradeResult is the outcome for one runner or ephemeral slot.
type UpgradeResult struct {
	Kind       string // KindPersistent | KindEphemeral
	Org, Name  string
	From, To   string
	Upgraded   bool
	RolledBack bool   // the upgrade failed its self-test and the runner was restored to From
	Skipped    string // non-empty reason (busy, already-at-target, dry-run, a safety gate)
	Err        error
}

// UpgradeLocalRunners upgrades the actions/runner AGENT on THIS host's runners
// to a target version, in place and idle-only. Persistent runners keep their
// registration (a binary swap, the agent's own auto-update mechanism); ephemeral
// lanes are rebuilt between cycles. It is SERIAL and continue-on-failure: each
// runner is fully upgraded (and self-tested back to active) or rolled back to its
// prior version before the next is touched, so at most one runner is ever offline
// and one host-specific failure never blocks the rest - a fundamentally bad
// target simply fails every runner and rolls each back to a working version.
//
// The state manifest supplies the recorded version (skip-if-at-target,
// anti-downgrade) and host-bound GitHub id (the gate that refuses to reclaim
// another host's same-named registration). It is advisory: an absent file or
// missing entry degrades to "version unknown => eligible, no gate", never to
// skipping a live runner. A CORRUPT manifest, by contrast, aborts the whole pass -
// proceeding blind would silently disable the downgrade and id gates.
//
// Must run as root on the host. Returns one result per local runner/slot plus a
// per-key error map (GitHub list failures, a corrupt manifest under "_state").
func (m *Manager) UpgradeLocalRunners(ctx context.Context, opts UpgradeOpts) ([]UpgradeResult, map[string]error) {
	return m.UpgradeLocalRunnersStream(ctx, opts, nil)
}

// UpgradeLocalRunnersStream is UpgradeLocalRunners with live per-runner streaming:
// each result is sent on progress (when non-nil) the moment that runner is fully
// upgraded or rolled back, so the TUI op panel reels them off one at a time rather
// than only at the end (decision #6). The serial, continue-on-failure contract is
// unchanged - the channel observes the same results the slice returns.
func (m *Manager) UpgradeLocalRunnersStream(ctx context.Context, opts UpgradeOpts, progress chan<- UpgradeResult) ([]UpgradeResult, map[string]error) {
	errs := map[string]error{}

	if opts.OrgFilter != "" {
		if _, ok := m.cfg.Org(opts.OrgFilter); !ok {
			errs["_arg"] = fmt.Errorf("org %q not configured", opts.OrgFilter)
			return nil, errs
		}
	}

	st, err := LoadState(StatePath)
	if err != nil {
		errs["_state"] = err
		return nil, errs
	}

	// Effective target version string (rollback resolves per-runner from cache).
	target := opts.ToVersion
	if target == "" {
		target = m.cfg.RunnerVersionPin
	}

	// Resolve the target Download once per org, memoized and shared by both passes
	// (the artifact is identical across orgs; the API call is org-scoped). Skipped
	// entirely in rollback mode, which resolves each runner's prior tarball locally.
	dlCache := map[string]runner.Download{}
	resolve := func(org string) (runner.Download, error) {
		if dl, ok := dlCache[org]; ok {
			return dl, nil
		}
		dl, err := m.resolveDownload(ctx, org, target)
		if err == nil {
			dlCache[org] = dl
		}
		return dl, err
	}

	var results []UpgradeResult
	results = append(results, m.upgradePersistent(ctx, opts, st, resolve, errs, progress)...)
	results = append(results, m.upgradeEphemeral(ctx, opts, errs, progress)...)

	if !opts.DryRun {
		_ = st.Save(StatePath) // belt-and-suspenders; the per-runner saves already persisted
	}
	return results, errs
}

// upgradePersistent walks the local persistent runners (live GitHub list filtered
// to this host, ephemeral JIT names excluded) and upgrades each.
func (m *Manager) upgradePersistent(ctx context.Context, opts UpgradeOpts, st *StateManifest, resolve func(string) (runner.Download, error), errs map[string]error, progress chan<- UpgradeResult) []UpgradeResult {
	all, listErrs := m.ListAllRunners(ctx)
	maps.Copy(errs, listErrs)
	var out []UpgradeResult
	for _, row := range all {
		if !row.Local || runner.IsEphemeralRunnerName(row.Runner.Name) {
			continue
		}
		if opts.OrgFilter != "" && row.Org != opts.OrgFilter {
			continue
		}
		if len(opts.Only) > 0 && !opts.Only[RunnerRef(row.Org, row.Runner.Name)] {
			continue // a bounded selection is active and this runner is not in it
		}
		res := m.upgradeOnePersistent(ctx, opts, st, resolve, row)
		out = append(out, res)
		if progress != nil {
			progress <- res
		}
		if !opts.DryRun {
			_ = st.Save(StatePath)
		}
	}
	return out
}

func (m *Manager) upgradeOnePersistent(ctx context.Context, opts UpgradeOpts, st *StateManifest, resolve func(string) (runner.Download, error), row RunnerWithOrg) UpgradeResult {
	org, name := row.Org, row.Runner.Name
	res := UpgradeResult{Kind: KindPersistent, Org: org, Name: name}
	orch := m.orchestratorFor(org)

	rec, ok := st.Get(KindPersistent, org, name)
	if !ok {
		rec = RunnerRecord{Kind: KindPersistent, Org: org, Name: name}
	}
	res.From = rec.AgentVersion

	if row.Runner.Busy {
		res.Skipped = "busy (job running)"
		return res
	}
	// Host-bound id gate: refuse if the live registration under this name is not the
	// one this host recorded. When the manifest has no recorded id (pre-v1.4 runner,
	// or a create where the id read failed) fall back to the authoritative id in the
	// agent's own .runner file - never treat "unrecorded" as "no gate", which would
	// let a stale local tree name-matched to a foreign live runner through. Checked
	// BEFORE resolving the target so a mismatch never even makes the download call.
	recordedID := rec.GitHubRunnerID
	if recordedID == 0 {
		if aid, err := orch.AgentID(org, name); err == nil {
			recordedID = aid
		}
	}
	if skip := idMismatchSkip(recordedID, row.Runner.ID); skip != "" {
		res.Skipped = skip
		return res
	}

	dl, stop := m.resolveTarget(orch, opts, resolve, org, &res, rec)
	if stop {
		return res
	}

	if !m.shouldProceed(opts, &res) {
		return res
	}
	if opts.DryRun {
		res.Skipped = fmt.Sprintf("dry-run (would upgrade %s -> %s)", orUnknown(res.From), res.To)
		return res
	}

	// Re-check live busy immediately before the swap: the busy flag above came from a
	// one-shot list snapshot taken before this serial pass began, so a job dispatched
	// to this (idle-at-snapshot) runner during the pass would otherwise be SIGTERM'd by
	// the stop. A transient GetRunner error is non-fatal - the swap's own stop is still
	// idle-gated by the snapshot, and we prefer progress to stalling on a flaky API.
	if c, cerr := m.client(ctx, org); cerr == nil {
		if live, gerr := c.GetRunner(ctx, org, row.Runner.ID); gerr == nil && live.Busy {
			res.Skipped = "became busy during the pass (re-run to upgrade it)"
			return res
		}
	}

	// UpgradeRunnerAgent is atomic from the caller's view: on failure it has already
	// rolled the runner back to its prior agent (a local payload-snapshot restore,
	// independent of the manifest or the download cache), unless the rollback itself
	// failed - which it signals with runner.ErrRunnerDown.
	prev := res.From
	if err := orch.UpgradeRunnerAgent(ctx, org, name, dl); err != nil {
		res.Err = err
		res.RolledBack = !errors.Is(err, runner.ErrRunnerDown)
		return res
	}

	rec.AgentVersion = res.To
	rec.PreviousVersion = prev
	rec.TemplateVersion = runner.CurrentDropInVersion
	rec.LastUpgradeAt = nowStamp()
	if rec.GitHubRunnerID == 0 && recordedID != 0 {
		rec.GitHubRunnerID = recordedID // adopt the authoritative .runner id
	}
	st.Put(rec)
	res.Upgraded = true
	return res
}

// upgradeEphemeral reports the host's ephemeral lanes as skipped. In-place ephemeral
// AGENT upgrade is deliberately NOT implemented in this release: a safe rebuild has to
// drain a possibly-mid-job lane, and the only idle signal available (the advisory
// jit-id) has a mint-before-record window plus a Restart=always race, so a naive
// teardown could SIGTERM a live job or orphan a single-use registration. Until a real
// drain/interlock lands, the supported way to move an ephemeral lane's agent is to
// recreate it (its next cycle re-extracts the current agent anyway). Enumerated rather
// than silently ignored so the operator sees they were considered.
func (m *Manager) upgradeEphemeral(ctx context.Context, opts UpgradeOpts, errs map[string]error, progress chan<- UpgradeResult) []UpgradeResult {
	// A bounded selection targets specific persistent runners; never sweep the
	// ephemeral lanes in that case.
	if len(opts.Only) > 0 {
		return nil
	}
	host := m.orchestratorFor("")
	units, err := host.ListEphemeralUnits(ctx)
	if err != nil {
		errs["_ephemeral"] = err
		return nil
	}
	orgsByLen := append([]string{}, m.cfg.OrgNames()...)
	sort.Slice(orgsByLen, func(i, j int) bool { return len(orgsByLen[i]) > len(orgsByLen[j]) })

	var out []UpgradeResult
	for _, svc := range units {
		org, slot, ok := parseEphemeralUnitName(svc, orgsByLen)
		if !ok || (opts.OrgFilter != "" && org != opts.OrgFilter) {
			continue
		}
		res := UpgradeResult{
			Kind: KindEphemeral, Org: org, Name: slot,
			Skipped: "ephemeral agent upgrade not supported yet - recreate the lane (`srm runners destroy --ephemeral --slot " + slot + "` then `srm runners create --ephemeral`) to refresh its agent",
		}
		out = append(out, res)
		if progress != nil {
			progress <- res
		}
	}
	return out
}

// resolveTarget fills res.To and returns the Download to install, or stop=true with
// res.Skipped/res.Err set when the runner should not be touched. It centralizes the
// target resolution shared by the persistent and ephemeral paths: rollback resolves
// the recorded prior version from the host cache (never an unverified fetch);
// forward resolves the pass target via the org client.
func (m *Manager) resolveTarget(orch runner.Orchestrator, opts UpgradeOpts, resolve func(string) (runner.Download, error), org string, res *UpgradeResult, rec RunnerRecord) (runner.Download, bool) {
	if opts.Rollback {
		if rec.PreviousVersion == "" {
			res.Skipped = "no recorded previous version to roll back to"
			return runner.Download{}, true
		}
		res.To = rec.PreviousVersion
		dl := runner.Download{URL: runner.DownloadURL(rec.PreviousVersion)} // SHA empty: cached-only
		if !orch.CachedAgentTarball(dl) {
			res.Err = fmt.Errorf("cannot roll back to %s: its tarball is no longer cached on this host", rec.PreviousVersion)
			return runner.Download{}, true
		}
		return dl, false
	}
	dl, err := resolve(org)
	if err != nil {
		res.Err = fmt.Errorf("resolve target: %w", err)
		return runner.Download{}, true
	}
	res.To = runner.VersionFromURL(dl.URL)
	return dl, false
}

// shouldProceed applies the version gates (skip-if-at-target, anti-downgrade) via
// the pure versionGateSkip, returning false with res.Skipped set when the runner is
// already where it should be or the move would be a downgrade.
func (m *Manager) shouldProceed(opts UpgradeOpts, res *UpgradeResult) bool {
	if skip := versionGateSkip(res.From, res.To, opts.Force, opts.Rollback); skip != "" {
		res.Skipped = skip
		return false
	}
	return true
}

// idMismatchSkip returns a skip reason when the recorded host-bound GitHub id and
// the live id for the same runner name disagree - the signal that another host in
// the org now owns this name, which upgrade (it preserves .runner) must never touch.
// It returns "" (proceed) when either id is unknown: the manifest is advisory, so an
// unrecorded id cannot gate, and a runner absent from the live list never reaches here.
func idMismatchSkip(recordedID, liveID int64) string {
	if recordedID != 0 && liveID != 0 && recordedID != liveID {
		return fmt.Sprintf("github id mismatch (live %d != recorded %d) - another host may own this name; refusing", liveID, recordedID)
	}
	return ""
}

// versionGateSkip is the pure, ORDER-SENSITIVE version decision: skip-if-at-target
// first, then anti-downgrade. It returns "" to proceed or a skip reason. --force
// overrides both gates; --rollback is an intentional downgrade, so the downgrade
// guard never applies to it (but the at-target guard still does, so a rollback to
// the running version is a no-op skip rather than a pointless reinstall). An unknown
// (empty) installed version disables both gates - the runner is always eligible.
func versionGateSkip(from, to string, force, rollback bool) string {
	if !force && from != "" && from == to {
		return "already at " + to
	}
	if !force && !rollback && from != "" && to != "" && runner.CompareVersions(to, from) < 0 {
		return fmt.Sprintf("target %s is older than installed %s (use --force, or --rollback)", to, from)
	}
	return ""
}

// orUnknown renders an empty version string as "unknown" for human-facing output.
func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

// AgentVersionStatus reports the agent version GitHub currently publishes for an
// org and how this host's recorded runners compare to it. It is best-effort and
// purely observational (it powers a `doctor` line): ok=false when the current
// version can't be resolved, total counts the local runners/slots with a recorded
// version (0 when run without root, since state.json is root-only), and behind
// lists those older than current. It NEVER mutates anything.
func (m *Manager) AgentVersionStatus(ctx context.Context, org string) (current string, total int, behind []string, ok bool) {
	dl, err := m.agentDownload(ctx, org)
	if err != nil {
		return "", 0, nil, false
	}
	current = runner.VersionFromURL(dl.URL)
	if current == "" {
		return "", 0, nil, false
	}
	st, err := LoadState(StatePath)
	if err != nil {
		return current, 0, nil, true // version known; manifest unreadable (e.g. non-root)
	}
	for _, rec := range st.Runners {
		if rec.Org != org || rec.AgentVersion == "" {
			continue
		}
		total++
		if runner.CompareVersions(rec.AgentVersion, current) < 0 {
			label := rec.Name
			if rec.Kind == KindEphemeral {
				label = "ephemeral/" + rec.Name
			}
			behind = append(behind, label)
		}
	}
	return current, total, behind, true
}
