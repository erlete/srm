package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/erlete/srm/internal/config"
)

// Windows ephemeral lanes.
//
// systemd's Restart=always (re-exec `srm _runner-cycle` after each clean exit) has
// no Service Control Manager analog: SCM treats a clean process exit as success and
// does not relaunch. So an ephemeral slot here is ONE long-lived Windows service
// running `srm-win _runner-supervisor --org X --slot N`, which loops
// Manager.RunCycle IN PROCESS - mint a single-use JIT config, run exactly one job,
// reap, repeat - until SCM stops it (see cli/supervisor_windows.go).
//
// Privilege model (single-user mode, matching the persistent Windows path): the
// supervisor service AND the jobs it runs share one low-privilege account
// (NETWORK SERVICE by default). Unlike the Linux unit - which starts as root to
// mint and DROPS to the per-org user only for run.sh - there is no mint/job
// privilege separation yet, so a job could in principle forge the slot's jit-id.
// That is an accepted single-user limitation; per-org isolation (a higher-priv
// supervisor + a token-dropped job) is a later phase and restores the separation.
//
// The slot's control files (jit-id, jit-params, the cycle counter) live in a
// per-slot dir UNDER %ProgramData%\srm\ephemeral, kept OUTSIDE the runner tree so
// the separation can be reinstated later without moving state. The service account
// is granted access to it today only because supervisor and job are the same
// account.

// defaultSelfExeWin is the fallback srm-win path baked into a supervisor service
// when Options.SelfExe is unset (os.Executable() failed). In practice SelfExe is
// always populated by the service layer, so this is a last resort.
const defaultSelfExeWin = `C:\Program Files\srm\srm-win.exe`

// winEphemeralStateRoot is srm's control-file root for ephemeral slots on Windows: a
// host path (NOT under installRoot) holding each slot's jit-id, jit-params, and cycle
// count. (The Linux build uses the const ephemeralStateRoot = /var/lib/srm/ephemeral;
// on Windows the root is derived from %ProgramData%\srm.)
func winEphemeralStateRoot() string { return filepath.Join(config.DataRoot(), "ephemeral") }

// winEphemeralControlDir is the per-slot control dir under winEphemeralStateRoot. It
// is a package func (not just a method) so the supervisor's cycle counter and the
// orchestrator's JIT ledger resolve to the exact same path.
func winEphemeralControlDir(org, slot string) string {
	return filepath.Join(winEphemeralStateRoot(), org, slot)
}

func cycleCountPath(org, slot string) string {
	return filepath.Join(winEphemeralControlDir(org, slot), "cycle-count")
}

func supervisorLogPath(org, slot string) string {
	return filepath.Join(winEphemeralControlDir(org, slot), "supervisor.log")
}

// LogEphemeralSupervisor appends a timestamped line to the slot's supervisor log -
// the Windows analog of journald for a lane. The supervisor runs detached as a
// service with NO console, so without this its per-cycle errors (a failed JIT mint, a
// run.cmd that won't start) would be invisible. Best-effort: a log write never
// affects a cycle. The log lives in the control dir, so it is removed with the lane.
func LogEphemeralSupervisor(org, slot, msg string) {
	p := supervisorLogPath(org, slot)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), msg)
}

// IncrementEphemeralCycle bumps the slot's cycle counter (the Windows stand-in for
// systemd NRestarts: there is no SCM restart per job, since the supervisor loops in
// process, so srm tracks the churn itself). Best-effort - a counter write must never
// fail a job cycle. Called by the supervisor loop after each RunCycle.
func IncrementEphemeralCycle(org, slot string) {
	p := cycleCountPath(org, slot)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(p, []byte(strconv.Itoa(EphemeralCycleCount(org, slot)+1)), 0o600)
}

// EphemeralCycleCount reads the slot's cycle counter (0 if absent/unreadable).
func EphemeralCycleCount(org, slot string) int {
	b, err := os.ReadFile(cycleCountPath(org, slot))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// ephemeralSlotDir is the warm agent tree for one slot lane, org-namespaced under
// installRoot (parity with the Linux layout: {installRoot}/{org}/.ephemeral/{slot}).
func (w *windows) ephemeralSlotDir(org, slot string) string {
	return filepath.Join(w.installRoot, org, ".ephemeral", slot)
}

// EphemeralDiagDir returns the slot lane's agent _diag directory (the Windows analog
// of the Linux path), where the runner writes per-job logs. See the interface doc.
func (w *windows) EphemeralDiagDir(org, slot string) string {
	return filepath.Join(w.ephemeralSlotDir(org, slot), "_diag")
}

func (w *windows) ephemeralControlDir(org, slot string) string {
	return winEphemeralControlDir(org, slot)
}

func (w *windows) jitIDPath(org, slot string) string {
	return filepath.Join(w.ephemeralControlDir(org, slot), "jit-id")
}

func (w *windows) jitParamsPath(org, slot string) string {
	return filepath.Join(w.ephemeralControlDir(org, slot), "jit-params")
}

// ephemeralSvcName is the Windows service name for a slot's supervisor:
// "actions.ephemeral.<org>.<slot>" (no ".service" - that is a systemd-ism, appended
// only by ListEphemeralUnits for the OS-agnostic reconcile name parser).
func (w *windows) ephemeralSvcName(org, slot string) string {
	return "actions.ephemeral." + org + "." + slot
}

// scServiceAccounts maps the built-in, passwordless accounts to the spelling
// `sc create ... obj=` accepts. The display-name form `NT AUTHORITY\NETWORK SERVICE`
// is not reliably accepted as a logon account by sc; the canonical forms are.
var scServiceAccounts = map[string]string{
	`NETWORK SERVICE`:              `NT AUTHORITY\NetworkService`,
	`NT AUTHORITY\NETWORK SERVICE`: `NT AUTHORITY\NetworkService`,
	`LOCAL SERVICE`:                `NT AUTHORITY\LocalService`,
	`NT AUTHORITY\LOCAL SERVICE`:   `NT AUTHORITY\LocalService`,
	`SYSTEM`:                       `LocalSystem`,
	`LOCAL SYSTEM`:                 `LocalSystem`,
	`LOCALSYSTEM`:                  `LocalSystem`,
	`NT AUTHORITY\SYSTEM`:          `LocalSystem`,
}

// scAccount returns the `sc create obj=` logon-account spelling for a service
// account: the canonical built-in form for the passwordless accounts, else verbatim.
func scAccount(user string) string {
	if a, ok := scServiceAccounts[strings.ToUpper(user)]; ok {
		return a
	}
	return user
}

// EnsureEphemeralSlot stands up (or replaces) one ephemeral slot lane: a warm agent
// tree, the persisted JIT mint params, and the supervisor Windows service (created +
// started). Idempotent: a prior lane of the same name is fully torn down first. Must
// run elevated.
func (w *windows) EnsureEphemeralSlot(ctx context.Context, spec EphemeralSlotSpec, dl Download) error {
	if err := w.EnsureBase(ctx); err != nil {
		return err
	}
	archive, err := w.ensureTarball(ctx, dl)
	if err != nil {
		return err
	}
	dir := w.ephemeralSlotDir(spec.Org, spec.Slot)
	svc := w.ephemeralSvcName(spec.Org, spec.Slot)

	// Fully tear down any prior lane + tree so the rebuild is idempotent.
	w.teardownEphemeralService(ctx, dir, svc)
	w.killAgentProcesses(ctx, dir)
	if err := removeRunnerTree(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := extractZip(archive, dir); err != nil {
		return fmt.Errorf("extract runner: %w", err)
	}
	// The service account writes _work/_diag/.runner under the slot tree each cycle;
	// grant Modify with inheritable ACEs before the supervisor starts.
	if err := w.grantServiceAccess(ctx, dir); err != nil {
		return fmt.Errorf("grant service-account access: %w", err)
	}

	// Persist the JIT mint params in the per-slot control dir (kept outside the slot
	// tree). Reset the cycle counter for a fresh lane.
	ctrl := w.ephemeralControlDir(spec.Org, spec.Slot)
	if err := os.MkdirAll(ctrl, 0o700); err != nil {
		return err
	}
	params, err := json.Marshal(EphemeralParams{GroupID: spec.GroupID, Labels: spec.Labels})
	if err != nil {
		return err
	}
	if err := os.WriteFile(w.jitParamsPath(spec.Org, spec.Slot), params, 0o600); err != nil {
		return err
	}
	_ = os.Remove(cycleCountPath(spec.Org, spec.Slot))
	// The supervisor (running as the service account) must read jit-params and write
	// jit-id + the counter, so grant the control dir too. In single-user mode this is
	// the same account that runs the job; per-org isolation later removes this grant.
	if err := w.grantServiceAccess(ctx, ctrl); err != nil {
		return fmt.Errorf("grant control-dir access: %w", err)
	}

	if err := w.installSupervisorService(spec.Org, spec.Slot, svc); err != nil {
		return err
	}
	// Inject the runner env onto the supervisor service BEFORE its first start: the
	// supervisor process inherits it from the SCM, and the run.cmd job processes it spawns
	// inherit it from the supervisor, so every job cycle sees the host tool cache + build
	// caches. Written before start, so no restart is needed (unlike the persistent path,
	// where config.cmd has already started the service).
	if err := writeServiceEnvironment(svc, winRunnerEnv(w.opts)); err != nil {
		return fmt.Errorf("set supervisor service environment: %w", err)
	}
	if err := w.startService(ctx, svc); err != nil {
		return err
	}
	// Self-test: the supervisor must report Running (it does so as soon as the loop
	// goroutine is launched, before run.cmd blocks waiting for a job).
	return w.waitServiceRunning(ctx, svc)
}

// teardownEphemeralService stops + deletes a slot's supervisor service (best-effort),
// waiting for the stop to settle (sc stop is async) and for the deletion to actually
// take effect before returning, so a re-create never hits "service marked for
// deletion".
func (w *windows) teardownEphemeralService(ctx context.Context, dir, svc string) {
	if w.serviceState(ctx, svc) == "" {
		return
	}
	_ = run(ctx, "sc.exe", "stop", svc)
	w.waitStopped(ctx, svc)
	_ = run(ctx, "sc.exe", "delete", svc)
	for i := 0; i < 30; i++ {
		if w.serviceState(ctx, svc) == "" {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// waitServiceRunning polls a service for ~10s and fails if it does not reach Running.
// Lighter than waitRunning's crash-loop self-test (~24s): a supervisor is healthy the
// moment it is Running, since it then blocks waiting for a job.
func (w *windows) waitServiceRunning(ctx context.Context, svc string) error {
	for i := 0; i < 20; i++ {
		switch st := w.serviceState(ctx, svc); {
		case strings.EqualFold(st, "Running"):
			return nil
		case st == "":
			return fmt.Errorf("service %s vanished after start", svc)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("service %s did not reach Running", svc)
}

// RemoveEphemeralSlot stops + deletes a slot's supervisor service and removes its
// tree and control dir. Best-effort; the caller deregisters any in-flight JIT id.
func (w *windows) RemoveEphemeralSlot(ctx context.Context, org, slot string) error {
	dir := w.ephemeralSlotDir(org, slot)
	svc := w.ephemeralSvcName(org, slot)
	w.teardownEphemeralService(ctx, dir, svc)
	w.killAgentProcesses(ctx, dir)
	_ = os.RemoveAll(w.ephemeralControlDir(org, slot))
	return removeRunnerTree(dir)
}

// EphemeralMintParams reads the slot's persisted JIT mint params (group + labels).
func (w *windows) EphemeralMintParams(org, slot string) (EphemeralParams, error) {
	var p EphemeralParams
	b, err := os.ReadFile(w.jitParamsPath(org, slot))
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(b, &p)
}

// PendingJIT returns the runner id recorded for an in-flight cycle, or 0.
func (w *windows) PendingJIT(org, slot string) int64 {
	b, err := os.ReadFile(w.jitIDPath(org, slot))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// RecordJIT persists (fsync'd) the just-minted runner id BEFORE the job runs, so a
// crash/reboot mid-job leaves a reapable id for the next cycle.
func (w *windows) RecordJIT(org, slot string, id int64) error {
	// Self-bootstrap the control dir so a cycle survives the dir having been removed.
	if err := os.MkdirAll(filepath.Dir(w.jitIDPath(org, slot)), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(w.jitIDPath(org, slot), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(strconv.FormatInt(id, 10)); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ClearJIT removes the recorded id after a clean cycle (no-op if absent).
func (w *windows) ClearJIT(org, slot string) error {
	if err := os.Remove(w.jitIDPath(org, slot)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RunJob resets the slot's per-job workspace (keeping the warm agent tree) and runs
// run.cmd for EXACTLY one job with the given JIT config. The agent runs as THIS
// process's account (the supervisor service's low-privilege account) - no privilege
// drop, because the whole supervisor already runs low-priv (unlike the Linux unit,
// which starts as root to mint and drops only for run.sh). The JIT blob is passed as
// the --jitconfig ARGUMENT (the agent does not read it from stdin); it is visible on
// the command line but single-use and short-lived.
func (w *windows) RunJob(ctx context.Context, org, slot, jitConfig string) error {
	dir := w.ephemeralSlotDir(org, slot)
	// Fresh per-job workspace: wipe the mutable layer and any spent JIT runtime creds
	// (they belong to a now-consumed registration), keeping the warm agent binaries.
	// run.cmd re-creates .runner/.credentials from the new JIT config each cycle.
	// _home is the fresh per-job profile (see winEphemeralJobEnv): wiped each cycle so
	// no profile-resident creds/state leak between ephemeral jobs.
	for _, sub := range []string{"_work", "_diag", "_home", ".runner", ".credentials", ".credentials_rsaparams"} {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
			return fmt.Errorf("reset %s: %w", sub, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "_work"), 0o750); err != nil {
		return err
	}
	// Pre-create the per-job profile skeleton so %APPDATA%/%LOCALAPPDATA%/%TEMP% exist
	// before any job step writes to them. MkdirAll of the two leaves creates the parents
	// (_home, AppData). grantServiceAccess below (recursive on dir) makes them writable
	// by the account the agent runs as.
	jobHome := filepath.Join(dir, "_home")
	for _, sub := range []string{filepath.Join("AppData", "Roaming"), filepath.Join("AppData", "Local", "Temp")} {
		if err := os.MkdirAll(filepath.Join(jobHome, sub), 0o750); err != nil {
			return err
		}
	}
	// Re-assert the service-account ACL so the freshly recreated _work/_home (and the
	// files run.cmd writes) are writable by the account the agent runs as.
	if err := w.grantServiceAccess(ctx, dir); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c",
		filepath.Join(dir, "run.cmd"), "--jitconfig", jitConfig)
	cmd.Dir = dir
	// Redirect the job's profile (USERPROFILE/APPDATA/...) to the fresh per-job _home so
	// it gets a clean slate, instead of sharing the supervisor account's profile across
	// cycles. Keeps the inherited service env (injected tool/cache vars, PATH).
	cmd.Env = winEphemeralJobEnv(os.Environ(), jobHome)
	// The supervisor runs detached as a service (no console), so tee the agent's
	// stdout/stderr to the slot's run log; without this the agent's own diagnostics on
	// a failed connect/config would be lost. Fall back to this process's streams when
	// run by hand (foreground diagnosis). The agent also writes detailed logs to _diag.
	var diag io.Writer = os.Stderr
	if lf, err := os.OpenFile(filepath.Join(w.ephemeralControlDir(org, slot), "run.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		defer lf.Close()
		cmd.Stdout, cmd.Stderr = lf, lf
		diag = lf
	} else {
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	}
	// Confine the job (and its whole process tree) to a Job Object carrying this slot's
	// resource caps for the job's duration; the supervisor holds the handle and reaps
	// the tree on close. With no caps configured this is exactly cmd.Run(), so the
	// validated default ephemeral path is unchanged. See runConfinedJob.
	return runConfinedJob(cmd, w.opts.Resources, diag)
}

// ListEphemeralUnits enumerates the host's ephemeral supervisor services from the
// SCM, returning them in the systemd-style "actions.ephemeral.<org>.<slot>.service"
// form the OS-agnostic reconcile name parser expects (the ".service" is appended
// here, exactly as ListUnits does for persistent runners).
func (w *windows) ListEphemeralUnits(ctx context.Context) ([]string, error) {
	names, err := w.listServices(ctx, "actions.ephemeral.")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, n+".service")
	}
	return out, nil
}

// InspectEphemeral reports host-side health for one slot lane from the SCM + the
// srm-tracked cycle counter. Windows has no drop-in/cgroup, so UnitOK is true
// (nothing to drift) and the memory metrics are -1 (unknown). Restarts is the cycle
// count (the supervisor loops in process, so there is no SCM restart per job).
func (w *windows) InspectEphemeral(ctx context.Context, org, slot string) EphemeralInspection {
	svc := w.ephemeralSvcName(org, slot)
	state := w.serviceState(ctx, svc)
	return EphemeralInspection{
		UnitExists:   state != "",
		Active:       strings.EqualFold(state, "Running"),
		Restarts:     EphemeralCycleCount(org, slot),
		UnitOK:       true,
		UnitVer:      CurrentEphemeralVersion,
		MemPeakBytes: -1,
		MemMaxBytes:  -1,
		MemCurBytes:  -1,
		OOMKills:     -1,
	}
}
