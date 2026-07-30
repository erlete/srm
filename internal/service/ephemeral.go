package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/erlete/srm/internal/core"
	ghub "github.com/erlete/srm/internal/github"
	"github.com/erlete/srm/internal/runner"
)

// EphemeralSlot is one host-side ephemeral slot lane's state for the TUI's
// Ephemeral panel. A slot is judged by HOST health alone (active + not crash-
// looping + conformant unit), never by a GitHub registration, which churns every
// job. Host-local: from a remote admin box none are returned.
type EphemeralSlot struct {
	Org      string
	Slot     string
	Active   bool
	Restarts int
	UnitOK   bool
	MemPeak  int64    // cgroup bytes, -1 unknown
	MemMax   int64    // cgroup bytes, -1 unlimited/unknown
	MemCur   int64    // cgroup live bytes, -1 unknown
	OOMKills int64    // cgroup memory.events oom_kill count, -1 unknown (0 = none)
	GroupID  int64    // runner group minted into this slot's JIT registrations (0 = unknown)
	Labels   []string // custom labels minted into this slot's JIT registrations
}

// IsEphemeralRunnerName reports whether a GitHub runner name was minted by an srm
// ephemeral slot. The persistent panel uses it to exclude these churning JIT
// registrations so the two runner natures are never shown side by side.
func IsEphemeralRunnerName(name string) bool { return runner.IsEphemeralRunnerName(name) }

// ListEphemeralSlots enumerates the ephemeral slot lanes installed on THIS host
// and inspects each (active state, restart count, drop-in conformance, memory).
// orgFilter (non-empty) scopes to one org. Host-local - slots only exist where
// they were created. Must run as root on the host for full inspection. Ordered by
// org, then slot number.
func (m *Manager) ListEphemeralSlots(ctx context.Context, orgFilter string) ([]EphemeralSlot, error) {
	host := m.orchestratorFor("")
	units, err := host.ListEphemeralUnits(ctx)
	if err != nil {
		return nil, err
	}
	orgsByLen := append([]string{}, m.cfg.OrgNames()...)
	sort.Slice(orgsByLen, func(i, j int) bool { return len(orgsByLen[i]) > len(orgsByLen[j]) })

	var out []EphemeralSlot
	for _, svc := range units {
		org, slot, ok := parseEphemeralUnitName(svc, orgsByLen)
		if !ok || (orgFilter != "" && org != orgFilter) {
			continue
		}
		orch := m.orchestratorFor(org)
		insp := orch.InspectEphemeral(ctx, org, slot)
		es := EphemeralSlot{
			Org: org, Slot: slot, Active: insp.Active, Restarts: insp.Restarts,
			UnitOK: insp.UnitOK, MemPeak: insp.MemPeakBytes, MemMax: insp.MemMaxBytes,
			MemCur: insp.MemCurBytes, OOMKills: insp.OOMKills,
		}
		// The slot's group + labels live in its persisted JIT mint params (host-only,
		// best-effort - a slot that never ran a cycle may not have them yet).
		if p, perr := orch.EphemeralMintParams(org, slot); perr == nil {
			es.GroupID, es.Labels = p.GroupID, p.Labels
		}
		out = append(out, es)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Org != out[j].Org {
			return out[i].Org < out[j].Org
		}
		return slotLess(out[i].Slot, out[j].Slot)
	})
	return out, nil
}

// occupiedEphemeralSlots returns the set of slot ids already installed for an org
// on THIS host (numeric ids only; srm only ever mints numeric slots). It backs the
// additive allocator so a repeat create never clobbers existing lanes.
func (m *Manager) occupiedEphemeralSlots(ctx context.Context, org string) (map[int]bool, error) {
	host := m.orchestratorFor("")
	units, err := host.ListEphemeralUnits(ctx)
	if err != nil {
		return nil, err
	}
	// Longest org name first so a shorter org that is a prefix of a longer one never
	// mis-claims a lane (mirrors ListEphemeralSlots / parseEphemeralUnitName).
	orgsByLen := append([]string{}, m.cfg.OrgNames()...)
	sort.Slice(orgsByLen, func(i, j int) bool { return len(orgsByLen[i]) > len(orgsByLen[j]) })

	occupied := map[int]bool{}
	for _, svc := range units {
		o, slot, ok := parseEphemeralUnitName(svc, orgsByLen)
		if !ok || o != org {
			continue
		}
		if n, cerr := strconv.Atoi(slot); cerr == nil {
			occupied[n] = true
		}
	}
	return occupied, nil
}

// allocateEphemeralSlots returns count NEW slot ids as the lowest free integers
// starting at 1, skipping any id in occupied. A gap left by a removed lane is
// reused before the range is extended, keeping slot ids dense.
func allocateEphemeralSlots(occupied map[int]bool, count int) []string {
	slots := make([]string, 0, count)
	for id := 1; len(slots) < count; id++ {
		if !occupied[id] {
			slots = append(slots, strconv.Itoa(id))
		}
	}
	return slots
}

// slotLess orders slot ids numerically when both parse as integers (so 2 sorts
// before 10), falling back to lexicographic order otherwise.
func slotLess(a, b string) bool {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	if aerr == nil && berr == nil {
		return ai < bi
	}
	return a < b
}

// CreateEphemeralRunners stands up Count ephemeral slot lanes on THIS host. Each
// slot is a systemd unit that mints a fresh single-use JIT registration per job
// (see RunCycle), so jobs run on a clean slate and registrations never linger.
// Must run as root on the target. Honors dry-run. Returns the slot ids created.
func (m *Manager) CreateEphemeralRunners(ctx context.Context, spec DeploySpec, progress chan<- core.ProgressEvent) ([]string, error) {
	org, err := m.requireOrg(spec.Org)
	if err != nil {
		return nil, err
	}
	if spec.Count < 1 {
		spec.Count = 1
	}
	// Fall back to the org's configured default labels when none were given
	// (OrgConfig.DefaultLabels, previously a dead field; see defaultLabels). The
	// default group is resolved just below via defaultGroupID.
	if len(spec.Labels) == 0 {
		spec.Labels = m.defaultLabels(org)
	}

	var groupID int64
	if spec.Group != "" {
		g, err := m.GetOrCreateGroup(ctx, org, spec.Group)
		if err != nil {
			return nil, fmt.Errorf("ensure group %q: %w", spec.Group, err)
		}
		groupID = g.ID
	}
	// JIT minting REQUIRES a real group id - runner_group_id:0 is rejected by the
	// API, which would make a slot hot-loop forever. Default to the org's configured
	// group, else the org "Default" group (id 1).
	if groupID == 0 {
		groupID = m.defaultGroupID(org)
	}

	dl, err := m.agentDownload(ctx, org)
	if err != nil {
		return nil, err
	}

	// Additive allocation: pick the next spec.Count FREE slot ids for this org, so a
	// repeat create ADDS lanes instead of clobbering slots 1..N. The old loop always
	// reused 1..Count and EnsureEphemeralSlot tears down + os.RemoveAll's the slot
	// dir, so "create 4" then "create 4" used to leave only 4 lanes. A gap freed by a
	// removed lane is reused before the id range is extended.
	occupied, err := m.occupiedEphemeralSlots(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("enumerate existing ephemeral slots: %w", err)
	}
	alloc := allocateEphemeralSlots(occupied, spec.Count)

	if m.cfg.DryRun {
		return alloc, nil
	}

	slots := make([]string, 0, spec.Count)
	orch := m.orchestratorFor(org)
	url := "https://github.com/" + org
	for i, slot := range alloc {
		espec := runner.EphemeralSlotSpec{Org: org, Slot: slot, URL: url, Labels: spec.Labels, GroupID: groupID}
		cerr := orch.EnsureEphemeralSlot(ctx, espec, dl)
		if progress != nil {
			msg := "created ephemeral slot " + slot
			if cerr != nil {
				msg = fmt.Sprintf("failed slot %s: %v", slot, cerr)
			}
			progress <- core.ProgressEvent{Index: i + 1, Total: spec.Count, Message: msg, Err: cerr}
		}
		if cerr != nil {
			return slots, fmt.Errorf("create ephemeral slot %s: %w", slot, cerr)
		}
		slots = append(slots, slot)
		// Record the lane's installed agent version (advisory; ephemeral lanes mint a
		// fresh JIT id per cycle, so there is no stable GitHub id to record).
		_ = m.recordCreated(RunnerRecord{
			Kind:            KindEphemeral,
			Org:             org,
			Name:            slot,
			AgentVersion:    runner.VersionFromURL(dl.URL),
			TemplateVersion: runner.CurrentEphemeralVersion,
		})
	}
	return slots, nil
}

// DestroyEphemeralSlot removes a slot lane from THIS host (stops + deletes its
// unit and tree) and deregisters any in-flight JIT registration from GitHub. Must
// run as root. Honors dry-run.
func (m *Manager) DestroyEphemeralSlot(ctx context.Context, org, slot string) error {
	org, err := m.requireOrg(org)
	if err != nil {
		return err
	}
	if !validSlot(slot) {
		return fmt.Errorf("invalid slot %q (must be a positive integer)", slot)
	}
	if m.cfg.DryRun {
		return nil
	}
	orch := m.orchestratorFor(org)
	// Deregister a still-registered runner before tearing the lane down so it
	// doesn't linger on GitHub as an offline ghost (404 = already gone, fine).
	if id := orch.PendingJIT(org, slot); id != 0 {
		if c, err := m.client(ctx, org); err == nil {
			if derr := c.DeleteRunner(ctx, org, id); derr != nil && !ghub.IsNotFound(derr) {
				return fmt.Errorf("deregister ephemeral runner %d: %w (reap later with `srm reconcile --reap-ephemeral`)", id, derr)
			}
		}
	}
	err = orch.RemoveEphemeralSlot(ctx, org, slot)
	// The lane is gone; drop its manifest entry (advisory).
	_ = m.forget(KindEphemeral, org, slot)
	return err
}

// RecreateEphemeralSlot tears a slot lane down and stands it back up on THIS host
// with the SAME slot id, labels, and group - the supported way to refresh a lane's
// agent (its next cycle re-extracts the current agent). The slot's mint params are
// captured BEFORE teardown removes them. Must run as root. Honors dry-run.
func (m *Manager) RecreateEphemeralSlot(ctx context.Context, org, slot string) error {
	org, err := m.requireOrg(org)
	if err != nil {
		return err
	}
	if !validSlot(slot) {
		return fmt.Errorf("invalid slot %q (must be a positive integer)", slot)
	}
	orch := m.orchestratorFor(org)
	// Capture labels + group before teardown (DestroyEphemeralSlot removes the lane).
	params, perr := orch.EphemeralMintParams(org, slot)
	if err := m.DestroyEphemeralSlot(ctx, org, slot); err != nil {
		return fmt.Errorf("destroy slot %s: %w", slot, err)
	}
	if m.cfg.DryRun {
		return nil
	}
	dl, err := m.agentDownload(ctx, org)
	if err != nil {
		return err
	}
	var labels []string
	gid := int64(0)
	if perr == nil {
		labels, gid = params.Labels, params.GroupID
	}
	if gid == 0 {
		gid = m.defaultGroupID(org)
	}
	espec := runner.EphemeralSlotSpec{Org: org, Slot: slot, URL: "https://github.com/" + org, Labels: labels, GroupID: gid}
	if err := orch.EnsureEphemeralSlot(ctx, espec, dl); err != nil {
		return fmt.Errorf("recreate slot %s: %w", slot, err)
	}
	_ = m.recordCreated(RunnerRecord{
		Kind: KindEphemeral, Org: org, Name: slot,
		AgentVersion: runner.VersionFromURL(dl.URL), TemplateVersion: runner.CurrentEphemeralVersion,
	})
	return nil
}

// defaultGroupID resolves the runner group for an ephemeral slot when none was
// given: the org's configured DefaultGroupID, else 1 (the org "Default" group).
// JIT minting requires a real group id - 0 is rejected by the API.
func (m *Manager) defaultGroupID(org string) int64 {
	if oc, ok := m.cfg.Org(org); ok && oc.DefaultGroupID > 0 {
		return oc.DefaultGroupID
	}
	return 1
}

// defaultLabels resolves the custom labels applied to a runner when the caller
// passed none: the org's configured DefaultLabels (nil if unset or unknown org).
// Wiring this is what makes OrgConfig.DefaultLabels actually take effect - it was
// written by onboard but never read at runtime until now. Shared by the persistent
// and ephemeral create paths.
func (m *Manager) defaultLabels(org string) []string {
	if oc, ok := m.cfg.Org(org); ok {
		return oc.DefaultLabels
	}
	return nil
}

// validSlot reports whether s is a numeric slot id - guards the root os.RemoveAll
// in the orchestrator against a traversal like "../../x" reaching it via filepath.Join.
func validSlot(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// RunCycle executes ONE ephemeral job cycle for a slot: reap any ghost left by a
// prior crashed cycle, mint a fresh single-use JIT config, run exactly one job as
// the per-org user, then return so the slot's systemd unit re-execs for the next
// job. Invoked by `srm _runner-cycle` (a slot unit's ExecStart) as root.
func (m *Manager) RunCycle(ctx context.Context, org, slot string) error {
	org, err := m.requireOrg(org)
	if err != nil {
		return err
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return err
	}
	orch := m.orchestratorFor(org)

	// Reap a ghost: a JIT runner auto-deregisters on success, so a recorded id that
	// is still present means a previous cycle minted but never finished cleanly
	// (crash/reboot between mint and completion). Deregister it before re-minting.
	if stale := orch.PendingJIT(org, slot); stale != 0 {
		_ = c.DeleteRunner(ctx, org, stale)
		_ = orch.ClearJIT(org, slot)
	}

	params, err := orch.EphemeralMintParams(org, slot)
	if err != nil {
		return fmt.Errorf("read slot params: %w", err)
	}
	// Clamp a 0 group id (e.g. stale .jit-params from before the default) so it
	// self-heals rather than hot-looping on a JIT API rejection.
	gid := params.GroupID
	if gid == 0 {
		gid = m.defaultGroupID(org)
	}
	// generate-jitconfig requires a non-null labels array of at least one item (GitHub
	// then layers self-hosted + OS + arch on top). A slot created without custom labels
	// has nil labels, which marshals to JSON null and is rejected with a 422; default to
	// self-hosted (GitHub's own documented example value) so the mint is valid and the
	// runner still ends up with the standard self-hosted/<os>/<arch> set. (Linux always
	// passes labels today, so this is a latent guard; it fires on a label-less slot.)
	labels := params.Labels
	if len(labels) == 0 {
		labels = []string{"self-hosted"}
	}
	runnerName := runner.EphemeralRunnerName(org, slot, cycleNonce())
	jit, err := c.GenerateJITConfig(ctx, org, ghub.JITRequest{
		Name:       runnerName,
		GroupID:    gid,
		Labels:     labels,
		WorkFolder: "_work",
	})
	if err != nil {
		backoffSleep(ctx)
		return fmt.Errorf("mint JIT config: %w", err)
	}
	// Record the id (fsync) BEFORE running, so a crash leaves a reapable ghost.
	if err := orch.RecordJIT(org, slot, jit.RunnerID); err != nil {
		_ = c.DeleteRunner(ctx, org, jit.RunnerID)
		backoffSleep(ctx)
		return fmt.Errorf("record jit id: %w", err)
	}

	startedAt := time.Now()
	runErr := orch.RunJob(ctx, org, slot, jit.EncodedJITConfig)
	// Durable job-log capture (item 8 Part A): copy this job's _diag log to the
	// root-owned store BEFORE the next cycle (or a teardown) wipes _diag. Best-effort;
	// runs whether the job passed or failed, so a failed run's log is captured too.
	m.captureJobLog(org, slot, runnerName, jit.RunnerID, startedAt, time.Now(), runErr)

	// Decide the registration's fate by its STATE, not run.sh's exit code (which can
	// be 0 even on a config failure). A real job auto-deregisters:
	//   404         -> gone (job ran): clear .jit-id.
	//   found (nil) -> still registered (job didn't run): deregister, clear on success.
	//   other error -> INCONCLUSIVE: keep .jit-id so the next cycle's PendingJIT reap
	//                  retries (clearing now would orphan the ghost on a transient error).
	_, gerr := c.GetRunner(ctx, org, jit.RunnerID)
	switch {
	case ghub.IsNotFound(gerr):
		_ = orch.ClearJIT(org, slot)
	case gerr == nil:
		if derr := c.DeleteRunner(ctx, org, jit.RunnerID); derr == nil || ghub.IsNotFound(derr) {
			_ = orch.ClearJIT(org, slot)
		}
		if runErr == nil {
			runErr = fmt.Errorf("runner still registered after the job - likely a misconfigured cycle")
		}
	}
	if runErr != nil {
		backoffSleep(ctx)
		return fmt.Errorf("run job: %w", runErr)
	}
	return nil
}

// cycleNonce returns a short unpredictable token so successive JIT registrations
// for one slot have distinct names.
func cycleNonce() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// backoffSleep waits a short jittered interval after a failed cycle so a broken
// slot can't hot-loop the GitHub API (the unit's RestartSec adds a floor on top,
// and reconcile's NRestarts threshold surfaces a persistently broken slot).
func backoffSleep(ctx context.Context) {
	jitter := time.Duration(time.Now().UnixNano()%5000) * time.Millisecond
	select {
	case <-time.After(5*time.Second + jitter):
	case <-ctx.Done():
	}
}
