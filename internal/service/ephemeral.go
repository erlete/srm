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
	MemPeak  int64 // cgroup bytes, -1 unknown
	MemMax   int64 // cgroup bytes, -1 unlimited/unknown
	MemCur   int64 // cgroup live bytes, -1 unknown
	OOMKills int64 // cgroup memory.events oom_kill count, -1 unknown (0 = none)
}

// IsEphemeralRunnerName reports whether a GitHub runner name was minted by an srm
// ephemeral slot. The persistent panel uses it to exclude these churning JIT
// registrations so the two runner natures are never shown side by side.
func IsEphemeralRunnerName(name string) bool { return runner.IsEphemeralRunnerName(name) }

// ListEphemeralSlots enumerates the ephemeral slot lanes installed on THIS host
// and inspects each (active state, restart count, drop-in conformance, memory).
// orgFilter (non-empty) scopes to one org. Host-local — slots only exist where
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
		insp := m.orchestratorFor(org).InspectEphemeral(ctx, org, slot)
		out = append(out, EphemeralSlot{
			Org: org, Slot: slot, Active: insp.Active, Restarts: insp.Restarts,
			UnitOK: insp.UnitOK, MemPeak: insp.MemPeakBytes, MemMax: insp.MemMaxBytes,
			MemCur: insp.MemCurBytes, OOMKills: insp.OOMKills,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Org != out[j].Org {
			return out[i].Org < out[j].Org
		}
		return slotLess(out[i].Slot, out[j].Slot)
	})
	return out, nil
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

	var groupID int64
	if spec.Group != "" {
		g, err := m.GetOrCreateGroup(ctx, org, spec.Group)
		if err != nil {
			return nil, fmt.Errorf("ensure group %q: %w", spec.Group, err)
		}
		groupID = g.ID
	}
	// JIT minting REQUIRES a real group id — runner_group_id:0 is rejected by the
	// API, which would make a slot hot-loop forever. Default to the org's configured
	// group, else the org "Default" group (id 1).
	if groupID == 0 {
		groupID = m.defaultGroupID(org)
	}

	dl, err := m.linuxDownload(ctx, org)
	if err != nil {
		return nil, err
	}

	slots := make([]string, 0, spec.Count)
	if m.cfg.DryRun {
		for i := 1; i <= spec.Count; i++ {
			slots = append(slots, strconv.Itoa(i))
		}
		return slots, nil
	}

	orch := m.orchestratorFor(org)
	url := "https://github.com/" + org
	for i := 1; i <= spec.Count; i++ {
		slot := strconv.Itoa(i)
		espec := runner.EphemeralSlotSpec{Org: org, Slot: slot, URL: url, Labels: spec.Labels, GroupID: groupID}
		cerr := orch.EnsureEphemeralSlot(ctx, espec, dl)
		if progress != nil {
			msg := "created ephemeral slot " + slot
			if cerr != nil {
				msg = fmt.Sprintf("failed slot %s: %v", slot, cerr)
			}
			progress <- core.ProgressEvent{Index: i, Total: spec.Count, Message: msg, Err: cerr}
		}
		if cerr != nil {
			return slots, fmt.Errorf("create ephemeral slot %s: %w", slot, cerr)
		}
		slots = append(slots, slot)
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
	return orch.RemoveEphemeralSlot(ctx, org, slot)
}

// defaultGroupID resolves the runner group for an ephemeral slot when none was
// given: the org's configured DefaultGroupID, else 1 (the org "Default" group).
// JIT minting requires a real group id — 0 is rejected by the API.
func (m *Manager) defaultGroupID(org string) int64 {
	if oc, ok := m.cfg.Org(org); ok && oc.DefaultGroupID > 0 {
		return oc.DefaultGroupID
	}
	return 1
}

// validSlot reports whether s is a numeric slot id — guards the root os.RemoveAll
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
	jit, err := c.GenerateJITConfig(ctx, org, ghub.JITRequest{
		Name:       runner.EphemeralRunnerName(org, slot, cycleNonce()),
		GroupID:    gid,
		Labels:     params.Labels,
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

	runErr := orch.RunJob(ctx, org, slot, jit.EncodedJITConfig)

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
			runErr = fmt.Errorf("runner still registered after the job — likely a misconfigured cycle")
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
