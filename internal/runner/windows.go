package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/erlete/srm/internal/config"
)

// windows is the Windows x64 Orchestrator: it manages the actions/runner agent via
// the Windows Service Control Manager (the runner's own config.cmd --runasservice
// installs the service) instead of systemd. It must run elevated (Administrator):
// installing a service, ACL-granting the runner tree, and SCM control all require it.
//
// It is a STANDALONE type (not an embedding of the Linux `ubuntu` orchestrator) so a
// systemd/POSIX method can never leak in by accident; it reuses only the genuinely
// OS-agnostic package helpers (verifySHA256, httpDownload, extractZip, the agent
// snapshot/rollback helpers, the .runner reader).
//
// Persistent runners are fully implemented. Ephemeral lanes, dep-cache prune, and the
// host slice are P2/P3: their methods are explicit "not yet on Windows" stubs rather
// than silent no-ops, so a caller never believes an unimplemented action succeeded.
type windows struct {
	installRoot string
	opts        Options
}

// NewWindows returns the Windows orchestrator rooted at installRoot.
func NewWindows(installRoot string, opts Options) Orchestrator {
	if opts.User == "" {
		opts.User = defaultServiceAccount
	}
	if opts.ToolCache == "" {
		opts.ToolCache = config.DefaultToolCacheRoot
	}
	// Mirror NewUbuntu: default the build-tool cache root so the runner-service env vars
	// resolve to a real Windows path. CacheRootFor returns "" in single-user mode, and an
	// empty root would make config.ToolCacheEnv yield root-anchored "\npm" paths scattered
	// across the system-drive root; default to %ProgramData%\srm\cache instead.
	if opts.CacheRoot == "" {
		opts.CacheRoot = config.DefaultCacheRoot
	}
	return &windows{installRoot: installRoot, opts: opts}
}

// defaultServiceAccount is the log-on account the runner service uses in single-user
// mode. NETWORK SERVICE is a low-privilege built-in with no password and a machine
// identity - the closest Windows analog to the Linux dedicated non-login user.
const defaultServiceAccount = `NT AUTHORITY\NETWORK SERVICE`

// serviceAccountSIDs maps the built-in, passwordless Windows service accounts to
// their well-known SIDs. icacls is given the "*SID" form for these rather than the
// account name because the display names are localized and not always resolvable,
// which fails with error 1332 ("no mapping between account names and security IDs").
// A SID needs no name lookup. Keyed upper-cased, with and without the authority
// prefix, so a config value in any common spelling resolves.
var serviceAccountSIDs = map[string]string{
	`NETWORK SERVICE`:              `*S-1-5-20`,
	`NT AUTHORITY\NETWORK SERVICE`: `*S-1-5-20`,
	`LOCAL SERVICE`:                `*S-1-5-19`,
	`NT AUTHORITY\LOCAL SERVICE`:   `*S-1-5-19`,
	`SYSTEM`:                       `*S-1-5-18`,
	`LOCAL SYSTEM`:                 `*S-1-5-18`,
	`LOCALSYSTEM`:                  `*S-1-5-18`,
	`NT AUTHORITY\SYSTEM`:          `*S-1-5-18`,
}

// icaclsGrantee returns the principal to hand icacls /grant for a service account:
// the well-known SID (the "*S-1-..." form) for the built-in passwordless accounts,
// whose localized names may fail to resolve (error 1332); otherwise the name
// verbatim, which icacls resolves itself for a real local or domain account.
func icaclsGrantee(user string) string {
	if sid, ok := serviceAccountSIDs[strings.ToUpper(user)]; ok {
		return sid
	}
	return user
}

// svcName is the Windows Service name config.cmd --runasservice assigns a runner:
// "actions.runner.<org>.<name>" (no ".service" suffix - that is a systemd-ism).
func (w *windows) svcName(org, name string) string {
	return "actions.runner." + org + "." + name
}

// runnerDir is the per-runner install tree, org-namespaced like the Linux layout.
func (w *windows) runnerDir(org, name string) string {
	return filepath.Join(w.installRoot, org, name)
}

func (w *windows) cachePath(dl Download) string {
	base := filepath.Base(dl.URL)
	if base == "" || base == "." || base == string(os.PathSeparator) {
		base = "actions-runner.zip"
	}
	return filepath.Join(agentCacheRoot, base)
}

// ensureTarball downloads + verifies the agent archive into the admin-only cache
// (same integrity contract as the Linux path; the archive is a .zip here).
func (w *windows) ensureTarball(ctx context.Context, dl Download) (string, error) {
	if err := os.MkdirAll(agentCacheRoot, 0o700); err != nil {
		return "", err
	}
	path := w.cachePath(dl)
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		if dl.SHA256 == "" {
			return path, nil
		}
		if ok, _ := verifySHA256(path, dl.SHA256); ok {
			return path, nil
		}
		_ = os.Remove(path)
	}
	if err := httpDownload(ctx, dl.URL, path); err != nil {
		return "", err
	}
	if dl.SHA256 != "" {
		if ok, sum := verifySHA256(path, dl.SHA256); !ok {
			_ = os.Remove(path)
			return "", fmt.Errorf("sha256 mismatch for %s: got %s want %s", dl.URL, sum, dl.SHA256)
		}
	}
	return path, nil
}

// EnsureBase creates the host directories the runners and caches live in. Unlike the
// Linux path it creates no OS user: single-user mode runs the services as the
// built-in NETWORK SERVICE account, and per-org isolation (local/virtual accounts) is
// a later phase. Idempotent.
func (w *windows) EnsureBase(ctx context.Context) error {
	for _, d := range []string{w.installRoot, w.opts.ToolCache, w.opts.CacheRoot, agentCacheRoot, config.DataRoot()} {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	// The runner runs as the low-priv service account, so it must read+write the shared
	// tool cache (setup-* writes new tool versions there) and the build-tool cache root
	// (npm/pnpm/go/... caches), whose locations are injected into the service env. Grant
	// Modify, inheritable. agentCacheRoot is deliberately left admin-only - its integrity
	// boundary is that job code must never write the agent archives srm extracts elevated.
	for _, d := range []string{w.opts.ToolCache, w.opts.CacheRoot} {
		if d == "" {
			continue
		}
		if err := w.grantServiceAccess(ctx, d); err != nil {
			return fmt.Errorf("grant cache access %s: %w", d, err)
		}
	}
	return nil
}

// winRunnerEnv is the environment srm injects into a runner's Windows service so jobs
// see the host-wide tool cache and host-persistent build-tool caches - the Windows
// analog of the Linux unit drop-in's Environment= lines (renderDropIn/writeEnvAndLimits).
// AGENT_TOOLSDIRECTORY points the agent's tool cache (RUNNER_TOOL_CACHE) at the shared
// C:\hostedtoolcache; without it the agent defaults to the per-runner _work\_tool and
// never sees what `srm provision` seeds. The rest point npm/pnpm/pip/go/cargo/gradle at
// the persistent cache root. Keys come from config.ToolCacheEnv (one source of truth with
// the Linux path); FromSlash normalizes the POSIX-built values to native backslashes.
// AGENT_TOOLSDIRECTORY is first so the list is deterministic.
func winRunnerEnv(opts Options) []string {
	env := []string{"AGENT_TOOLSDIRECTORY=" + filepath.FromSlash(opts.ToolCache)}
	for _, e := range config.ToolCacheEnv(opts.CacheRoot) {
		env = append(env, e.Key+"="+filepath.FromSlash(e.Val))
	}
	return env
}

// winProfileVars are the environment variables that resolve to the per-user profile on
// Windows. An ephemeral job that inherits the supervisor account's (NETWORK SERVICE)
// profile shares them across cycles, so profile-resident credentials/state leak between
// jobs - the Windows analog of the Linux setpriv lane's HOME problem. winEphemeralJobEnv
// re-points these at a fresh per-job dir; this is the canonical set to strip first.
var winProfileVars = []string{"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP"}

// winEphemeralJobEnv gives an ephemeral Windows job a FRESH, per-cycle profile instead
// of sharing the supervisor account's roaming profile across jobs. Without it, creds and
// state written under the profile (%USERPROFILE%\.gitconfig, .netrc, %APPDATA%\npm
// .npmrc auth, %USERPROFILE%\.docker\config.json) persist between ephemeral cycles - the
// Windows analog of the Linux lane's per-job _home wipe (jobs run as NETWORK SERVICE with
// no privilege drop, so USERNAME is already correct; only the profile location needs
// redirecting). It starts from base (the inherited service env, so the injected
// AGENT_TOOLSDIRECTORY/cache vars and PATH/SystemRoot survive), drops the profile vars
// (case-insensitive - Windows env names are), and re-points them under jobHome, which
// RunJob wipes each cycle. Build-tool CACHES stay on their persistent cache root (they
// are separate vars, not under the profile), so only per-job creds/state are reset.
func winEphemeralJobEnv(base []string, jobHome string) []string {
	roaming := filepath.Join(jobHome, "AppData", "Roaming")
	local := filepath.Join(jobHome, "AppData", "Local")
	temp := filepath.Join(local, "Temp")
	vol := filepath.VolumeName(jobHome) // "C:"
	redirect := map[string]string{
		"USERPROFILE":  jobHome,
		"HOMEDRIVE":    vol,
		"HOMEPATH":     strings.TrimPrefix(jobHome, vol),
		"APPDATA":      roaming,
		"LOCALAPPDATA": local,
		"TEMP":         temp,
		"TMP":          temp,
	}
	drop := map[string]bool{}
	for _, k := range winProfileVars {
		drop[k] = true
	}
	out := make([]string, 0, len(base)+len(winProfileVars))
	for _, kv := range base {
		k := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		if drop[strings.ToUpper(k)] {
			continue // inherited profile var; replaced with a per-job value below
		}
		out = append(out, kv)
	}
	// Append the redirected vars in a fixed order so the result is deterministic (tests).
	for _, k := range winProfileVars {
		out = append(out, k+"="+redirect[k])
	}
	return out
}

// CreateRunner downloads + verifies + extracts the agent, configures and installs it
// as a Windows Service via config.cmd --runasservice, grants the service account
// access to the tree, starts it, and self-tests. Idempotent: an existing runner of
// the same name is torn down first. Must run elevated.
func (w *windows) CreateRunner(ctx context.Context, spec RunnerSpec, dl Download, regToken string) error {
	if err := w.EnsureBase(ctx); err != nil {
		return err
	}
	archive, err := w.ensureTarball(ctx, dl)
	if err != nil {
		return err
	}
	dir := w.runnerDir(spec.Org, spec.Name)
	svc := w.svcName(spec.Org, spec.Name)

	w.teardownService(ctx, dir, svc)
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
	// The service account needs to write _work/_diag/.runner/.credentials under the
	// tree; grant it Modify before the agent starts.
	if err := w.grantServiceAccess(ctx, dir); err != nil {
		return fmt.Errorf("grant service-account access: %w", err)
	}

	// config.cmd registers the runner AND installs+starts the Windows Service.
	// --disableupdate keeps srm the version authority (mirrors the Linux build).
	args := []string{
		"--unattended", "--replace", "--disableupdate",
		"--url", spec.URL, "--token", regToken,
		"--name", spec.Name, "--labels", strings.Join(spec.Labels, ","),
		"--work", "_work", "--runasservice",
	}
	if spec.Group != "" {
		args = append(args, "--runnergroup", spec.Group)
	}
	if out, err := w.configCmd(ctx, dir, args...); err != nil {
		return fmt.Errorf("config.cmd: %w: %s", err, redactToken(out, regToken))
	}

	// config.cmd --runasservice installs AND starts the service, but with no custom
	// environment. Inject the runner env (host tool cache + build caches) onto the
	// service, then restart so the SCM rebuilds the agent's process environment with it -
	// the SCM reads the per-service Environment value only at start. Without this the
	// agent's RUNNER_TOOL_CACHE falls back to the per-runner _work\_tool and never sees
	// the shared C:\hostedtoolcache (the Windows analog of the Linux drop-in env).
	if err := writeServiceEnvironment(svc, winRunnerEnv(w.opts)); err != nil {
		return fmt.Errorf("set runner service environment: %w", err)
	}
	// config.cmd already started the service; wait for it to actually reach Running BEFORE
	// stopping it. sc stop on a still-START_PENDING service is rejected by the SCM (1061),
	// which would abort an otherwise-healthy create AND leave the agent running without the
	// environment just written. waitServiceRunning returns as soon as it is Running (a fast
	// poll, unlike the ~24s crash-loop self-test waitRunning below).
	if err := w.waitServiceRunning(ctx, svc); err != nil {
		return err
	}
	if err := w.stopService(ctx, svc); err != nil {
		return err
	}
	if err := w.startService(ctx, svc); err != nil {
		return err
	}
	// Self-test that it stays up after the env-carrying restart (does not immediately crash).
	return w.waitRunning(ctx, svc)
}

// UpgradeRunnerAgent swaps a runner's agent payload in place, preserving its
// registration. Mirrors the Linux rename-snapshot rollback exactly (the snapshot
// helpers are OS-agnostic); only stop/start go through the SCM and extraction is zip.
func (w *windows) UpgradeRunnerAgent(ctx context.Context, org, name string, dl Download) error {
	svc := w.svcName(org, name)
	dir := w.runnerDir(org, name)
	if w.serviceState(ctx, svc) == "" {
		return fmt.Errorf("no installed service for %s/%s on this host", org, name)
	}
	if !fileExists(filepath.Join(dir, ".runner")) {
		return fmt.Errorf("%s/%s has no .runner (not configured) - create it instead of upgrading", org, name)
	}
	if dl.SHA256 == "" && !w.CachedAgentTarball(dl) {
		return fmt.Errorf("refusing to upgrade %s/%s to an unverifiable agent: %s has no published checksum and is not in the host cache", org, name, filepath.Base(dl.URL))
	}
	archive, err := w.ensureTarball(ctx, dl)
	if err != nil {
		return err
	}

	recoverAgentSnapshot(dir)
	if err := w.stopService(ctx, svc); err != nil {
		return fmt.Errorf("stop %s: %w", svc, err)
	}
	// Release the DLL locks the just-stopped agent may still hold before renaming the
	// tree: a stopped service does not guarantee its child processes have exited, and
	// Windows refuses to rename a directory holding an open file (bin\clrjit.dll).
	w.killAgentProcesses(ctx, dir)
	if err := snapshotAgentPayload(dir); err != nil {
		_ = w.startService(ctx, svc)
		return fmt.Errorf("snapshot current agent: %w", err)
	}
	swapErr := func() error {
		if err := extractZip(archive, dir); err != nil {
			return fmt.Errorf("extract runner: %w", err)
		}
		if err := w.grantServiceAccess(ctx, dir); err != nil {
			return err
		}
		if err := w.startService(ctx, svc); err != nil {
			return err
		}
		return w.waitRunning(ctx, svc)
	}()
	if swapErr != nil {
		_ = w.stopService(ctx, svc)
		// Same lock release before restoreAgentPayload removes the freshly-extracted
		// bin\ and renames the snapshot back.
		w.killAgentProcesses(ctx, dir)
		restoreErr := restoreAgentPayload(dir)
		_ = w.startService(ctx, svc)
		if restoreErr != nil {
			return fmt.Errorf("upgrade failed (%v); rollback also failed (%v): %w", swapErr, restoreErr, ErrRunnerDown)
		}
		return fmt.Errorf("upgrade failed and was rolled back to the prior agent: %w", swapErr)
	}
	discardAgentSnapshot(dir)
	return nil
}

// CachedAgentTarball reports whether dl's archive is already in the host cache,
// verifying its checksum when known.
func (w *windows) CachedAgentTarball(dl Download) bool {
	path := w.cachePath(dl)
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return false
	}
	if dl.SHA256 != "" {
		ok, _ := verifySHA256(path, dl.SHA256)
		return ok
	}
	return true
}

// RemoveRunner stops + deletes the service, deregisters (if a remove token is given),
// and deletes the tree. Best-effort; the API delete is the GitHub source of truth.
func (w *windows) RemoveRunner(ctx context.Context, name, org, removeToken string) error {
	dir := w.runnerDir(org, name)
	svc := w.svcName(org, name)
	w.teardownService(ctx, dir, svc)
	if removeToken != "" && fileExists(filepath.Join(dir, "config.cmd")) {
		_, _ = w.configCmd(ctx, dir, "remove", "--token", removeToken)
	}
	w.killAgentProcesses(ctx, dir)
	return removeRunnerTree(dir)
}

// killAgentProcesses force-terminates every process whose executable image lives
// under dir - the agent's Runner.Listener.exe / Runner.Worker.exe and anything they
// or config.cmd spawned - then WAITS for them to exit, so the DLLs they have loaded
// (notably bin\clrjit.dll) are released before the tree is mutated. Without this, a
// process exiting a moment too late keeps the DLL locked and the next tree operation
// fails with "Access is denied": removeRunnerTree on destroy/re-create, and the
// snapshot/rollback RENAME of bin\ on upgrade (Windows refuses to rename a directory
// holding an open file). sc stop reporting Stopped does NOT guarantee the agent's
// child processes have torn down, hence the explicit kill + Wait-Process. Best-effort
// and tightly scoped: the StartsWith match (with the trailing separator) only ever
// targets processes inside THIS runner's tree, never a sibling runner. Elevated-only,
// but every write path that calls it already requires Administrator.
func (w *windows) killAgentProcesses(ctx context.Context, dir string) {
	prefix := dir + `\`
	script := fmt.Sprintf(`$ps = Get-CimInstance Win32_Process | `+
		`Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith('%s',[System.StringComparison]::OrdinalIgnoreCase) }; `+
		`$ps | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }; `+
		`$ps | ForEach-Object { Wait-Process -Id $_.ProcessId -Timeout 15 -ErrorAction SilentlyContinue }`, psQuote(prefix))
	_, _ = w.psOutput(ctx, script)
}

// removeRunnerTree deletes a runner directory, retrying transient Windows file
// locks. Even after the runner process exits, Windows (and antivirus) can briefly
// hold a handle to a just-unloaded DLL, and a read-only attribute makes a file
// undeletable - so a plain os.RemoveAll can fail with "Access is denied" (seen on
// bin\clrjit.dll). Each pass clears read-only bits and retries with backoff. There
// is no Linux analog: POSIX unlink does not block on open files.
func removeRunnerTree(dir string) error {
	var err error
	for i := 0; i < 10; i++ {
		clearReadOnly(dir)
		if err = os.RemoveAll(dir); err == nil || !fileExists(dir) {
			return nil
		}
		time.Sleep(time.Duration(250*(i+1)) * time.Millisecond)
	}
	return err
}

// clearReadOnly best-effort clears the read-only attribute on every entry under
// dir so RemoveAll can delete them (Windows refuses to delete a read-only file).
func clearReadOnly(dir string) {
	_ = filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		if err == nil {
			_ = os.Chmod(p, 0o600)
		}
		return nil
	})
}

// AgentID reads the GitHub runner id from the agent's .runner file (UTF-8 with a BOM,
// like the Linux agent). Host-local, host-bound identity for deregister-by-id.
func (w *windows) AgentID(org, name string) (int64, error) {
	data, err := os.ReadFile(filepath.Join(w.runnerDir(org, name), ".runner"))
	if err != nil {
		return 0, err
	}
	var dr dotRunner
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}), &dr); err != nil {
		return 0, fmt.Errorf("parse .runner: %w", err)
	}
	if dr.AgentID == 0 {
		return 0, fmt.Errorf(".runner has no agentId")
	}
	return dr.AgentID, nil
}

// RefreshUnit re-applies the runner service environment (host tool cache + build caches)
// and restarts the service so the SCM reloads it - the Windows analog of the Linux
// RefreshUnit rewriting the systemd drop-in and restarting. The SCM reads the per-service
// Environment value only at start, so the restart is what makes a changed value take effect.
func (w *windows) RefreshUnit(ctx context.Context, org, name string) error {
	svc := w.svcName(org, name)
	if w.serviceState(ctx, svc) == "" {
		return fmt.Errorf("no installed service for %s/%s on this host", org, name)
	}
	if err := writeServiceEnvironment(svc, winRunnerEnv(w.opts)); err != nil {
		return fmt.Errorf("set runner service environment: %w", err)
	}
	if err := w.stopService(ctx, svc); err != nil {
		return err
	}
	if err := w.startService(ctx, svc); err != nil {
		return err
	}
	return w.waitRunning(ctx, svc)
}

// Inspect reports host-side state for one runner from the SCM + the on-disk tree.
// Windows has no drop-in/cgroup, so DropInOK is reported true (nothing to drift) and
// the cgroup metrics are -1 (unknown), which the OS-agnostic reconcile classifier
// already renders gracefully.
func (w *windows) Inspect(ctx context.Context, org, name string) Inspection {
	dir := w.runnerDir(org, name)
	svc := w.svcName(org, name)
	state := w.serviceState(ctx, svc)
	return Inspection{
		UnitExists:   state != "",
		Active:       strings.EqualFold(state, "Running"),
		HasTree:      fileExists(filepath.Join(dir, "config.cmd")),
		HasFlatTree:  fileExists(filepath.Join(w.installRoot, name, "config.cmd")),
		User:         w.opts.User,
		DropInOK:     true, // no drop-in on Windows; never report false drift
		DropInVer:    CurrentDropInVersion,
		MemPeakBytes: -1,
		MemMaxBytes:  -1,
		MemCurBytes:  -1,
		OOMKills:     -1,
	}
}

// ListUnits enumerates srm-managed runner services from the SCM. It returns names in
// the systemd-style "actions.runner.<org>.<name>.service" form so the OS-agnostic
// reconcile name parser works unchanged (the trailing ".service" is appended here).
func (w *windows) ListUnits(ctx context.Context) ([]string, error) {
	names, err := w.listServices(ctx, "actions.runner.")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, n+".service")
	}
	return out, nil
}

// --- host health (read-only) ---

// DirSize totals a directory's regular-file bytes (portable WalkDir), -1 on error.
func (w *windows) DirSize(ctx context.Context, path string) int64 {
	var total int64
	err := filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			if info, e := d.Info(); e == nil {
				total += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		return -1
	}
	return total
}

// HostDisk reports volume usage for each path's drive via PowerShell Get-PSDrive.
// Best-effort: a path whose drive can't be read is omitted.
func (w *windows) HostDisk(ctx context.Context, paths []string) []DiskStat {
	var out []DiskStat
	for _, p := range paths {
		vol := filepath.VolumeName(p) // e.g. "C:"
		if vol == "" {
			continue
		}
		drive := strings.TrimSuffix(vol, ":")
		ps := fmt.Sprintf(`$d = Get-PSDrive -Name '%s' -ErrorAction SilentlyContinue; if ($d) { "{0} {1}" -f $d.Used, $d.Free }`, drive)
		o, err := w.psOutput(ctx, ps)
		if err != nil {
			continue
		}
		var used, free int64
		if _, err := fmt.Sscanf(strings.TrimSpace(o), "%d %d", &used, &free); err != nil {
			continue
		}
		total := used + free
		pct := 0
		if total > 0 {
			pct = int(used * 100 / total)
		}
		out = append(out, DiskStat{Path: p, TotalBytes: total, UsedBytes: used, AvailBytes: free, UsePct: pct})
	}
	return out
}

// SliceUsage has no Windows analog (no host-wide cgroup slice). Reports unknown.
func (w *windows) SliceUsage(ctx context.Context) (current, max int64) { return -1, -1 }

// --- ephemeral lanes: see windows_ephemeral.go (the supervisor-service model) ---
//
// Windows has no systemd Restart=always, so an ephemeral slot is one long-lived
// service running `srm-win _runner-supervisor`, which loops Manager.RunCycle in
// process. EnsureEphemeralSlot / RemoveEphemeralSlot / RunJob / the JIT ledger /
// ListEphemeralUnits / InspectEphemeral all live in windows_ephemeral.go.

// --- dep cache + purge: P3 stubs ---

// PruneDepCache is a no-op on Windows for now (the per-tool cache layout differs).
func (w *windows) PruneDepCache(ctx context.Context, maxAge time.Duration, dryRun bool) (PruneStats, error) {
	return PruneStats{Root: w.opts.CacheRoot}, nil
}

// PurgeBase removes the given paths (recursively) for `srm uninstall`. Service and
// user removal is the caller's province on Linux; on Windows services are removed by
// RemoveRunner, so this just clears the host directories. Aggregates failures.
func (w *windows) PurgeBase(ctx context.Context, p BasePurge) error {
	var errs []string
	for _, path := range p.Paths {
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Sprintf("remove %s: %v", path, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// --- SCM + config.cmd plumbing ---

// configCmd runs the runner's config.cmd in dir with the given args and returns its
// combined output. config.cmd is a batch file, so it is invoked via cmd.exe /c.
func (w *windows) configCmd(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"/c", filepath.Join(dir, "config.cmd")}, args...)
	c := exec.CommandContext(ctx, "cmd.exe", full...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// serviceState returns the Windows service status ("Running"/"Stopped"/...) or "" if
// the service does not exist.
func (w *windows) serviceState(ctx context.Context, svc string) string {
	o, err := w.psOutput(ctx, fmt.Sprintf(`(Get-Service -Name '%s' -ErrorAction SilentlyContinue).Status`, psQuote(svc)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(o)
}

func (w *windows) startService(ctx context.Context, svc string) error {
	if err := run(ctx, "sc.exe", "start", svc); err != nil {
		// sc start returns non-zero if already running; tolerate that.
		if !strings.EqualFold(w.serviceState(ctx, svc), "Running") {
			return fmt.Errorf("start service %s: %w", svc, err)
		}
	}
	return nil
}

// stopService issues sc.exe stop and WAITS for the service to actually reach Stopped.
// sc.exe stop is asynchronous - it only sends the control and returns immediately, so
// the agent process (and the managed DLLs it has loaded) keeps running for a moment
// afterward. Every caller either mutates the tree right after (upgrade's snapshot
// rename, which Windows refuses while a file under bin\ is open) or restarts the
// service (refresh, where a start issued during STOP_PENDING is rejected), so the wait
// is mandatory, not best-effort.
func (w *windows) stopService(ctx context.Context, svc string) error {
	err := run(ctx, "sc.exe", "stop", svc)
	w.waitStopped(ctx, svc)
	if err != nil && strings.EqualFold(w.serviceState(ctx, svc), "Running") {
		return fmt.Errorf("stop service %s: %w", svc, err)
	}
	return nil
}

// teardownService stops and deletes a runner's Windows service (best-effort) so a
// re-create is idempotent. It waits for the process to actually stop between stop
// and delete: sc.exe stop only SENDS the control and returns immediately, so the
// runner process keeps running (holding its DLLs) for a moment afterward.
func (w *windows) teardownService(ctx context.Context, dir, svc string) {
	if w.serviceState(ctx, svc) != "" {
		_ = run(ctx, "sc.exe", "stop", svc)
		w.waitStopped(ctx, svc)
		_ = run(ctx, "sc.exe", "delete", svc)
	}
}

// waitStopped blocks until the service has actually stopped (or is gone), up to
// ~15s. sc.exe stop is asynchronous, so the runner process - and the managed
// assemblies it has loaded, notably bin\clrjit.dll - keeps running briefly after
// stop returns. Deleting the tree before the process exits fails with "Access is
// denied" on the locked DLL, so callers wait here first.
func (w *windows) waitStopped(ctx context.Context, svc string) {
	for i := 0; i < 30; i++ {
		if st := w.serviceState(ctx, svc); st == "" || strings.EqualFold(st, "Stopped") {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// waitRunning polls a service for ~24s and fails if it does not reach (and stay)
// Running - the SCM analog of the Linux waitActive crash-loop check.
func (w *windows) waitRunning(ctx context.Context, svc string) error {
	for i := 0; i < 12; i++ {
		time.Sleep(2 * time.Second)
		switch st := w.serviceState(ctx, svc); {
		case st == "":
			return fmt.Errorf("service %s vanished after start", svc)
		case strings.EqualFold(st, "Stopped"):
			return fmt.Errorf("service %s stopped after start (agent likely crashed)", svc)
		}
	}
	if !strings.EqualFold(w.serviceState(ctx, svc), "Running") {
		return fmt.Errorf("service %s not running after start", svc)
	}
	return nil
}

// grantServiceAccess grants the runner's service account Modify on its tree via
// icacls, so the low-privilege account can write _work/_diag/.runner. The grant is
// applied BEFORE config.cmd writes the tree, with inheritable ACEs ((OI)(CI)), so
// files the agent creates later inherit the access too. The grantee is resolved
// through icaclsGrantee so the built-in service accounts go in by SID.
func (w *windows) grantServiceAccess(ctx context.Context, dir string) error {
	grant := fmt.Sprintf(`%s:(OI)(CI)M`, icaclsGrantee(w.opts.User))
	return run(ctx, "icacls", dir, "/grant", grant, "/T", "/C", "/Q")
}

// listServices returns the names of Windows services whose name starts with prefix.
func (w *windows) listServices(ctx context.Context, prefix string) ([]string, error) {
	ps := fmt.Sprintf(`Get-Service -Name '%s*' -ErrorAction SilentlyContinue | ForEach-Object { $_.Name }`, psQuote(prefix))
	o, err := w.psOutput(ctx, ps)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(o, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			names = append(names, s)
		}
	}
	return names, nil
}

// psOutput runs a PowerShell snippet and returns its stdout.
func (w *windows) psOutput(ctx context.Context, script string) (string, error) {
	c := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := c.Output()
	return string(out), err
}

// psQuote escapes single quotes for embedding inside a single-quoted PowerShell
// string (PowerShell doubles the quote to escape it).
func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }
