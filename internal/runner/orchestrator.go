package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/erlete/srm/internal/config"
)

// ErrRunnerDown is wrapped into the error UpgradeRunnerAgent returns when an upgrade
// failed AND the subsequent rollback to the prior agent also failed - i.e. the runner
// is left stopped/broken. A returned error WITHOUT this sentinel means the upgrade
// failed but the runner was successfully restored to its prior agent (still serving).
// Callers use errors.Is to tell "rolled back, fine" from "needs hands-on recovery".
var ErrRunnerDown = errors.New("runner is down (rollback failed)")

// AggregateSlice is the systemd slice that holds every srm runner so their
// memory is bounded together (auto-capacity mode). See Options.Slice.
const AggregateSlice = "srm.slice"

// Ephemeral runner naming. An ephemeral SLOT is a fixed systemd lane that mints a
// fresh single-use JIT registration for every job. Two stable srm markers let
// reconcile recognize the family WITHOUT a 1:1 unit↔registration join (the
// registration name changes every job, so such a join is impossible):
//   - EphemeralUnitPrefix tags the persistent slot unit: actions.ephemeral.<org>.<slot>.service
//   - EphemeralNamePrefix tags every minted JIT runner name: srm-eph-<org>-<slot>-<token>
const (
	EphemeralUnitPrefix = "actions.ephemeral."
	EphemeralNamePrefix = "srm-eph-"
)

// DefaultSelfExe is where srm is installed on the host; an ephemeral slot unit's
// ExecStart calls it. Overridable via Options.SelfExe (e.g. os.Executable()).
const DefaultSelfExe = "/usr/local/bin/srm"

// EphemeralSvcName is the systemd unit name for an ephemeral slot lane.
func EphemeralSvcName(org, slot string) string {
	return fmt.Sprintf("%s%s.%s.service", EphemeralUnitPrefix, org, slot)
}

// EphemeralRunnerName builds the JIT runner name for one job cycle. token is a
// caller-supplied per-cycle nonce that keeps successive registrations distinct;
// the stable prefix + org + slot let reconcile attribute the registration to a
// slot family even though the full name differs every job.
func EphemeralRunnerName(org, slot, token string) string {
	return fmt.Sprintf("%s%s-%s-%s", EphemeralNamePrefix, org, slot, token)
}

// IsEphemeralRunnerName reports whether a GitHub runner name was minted by an srm
// ephemeral slot. reconcile uses it to exempt these from orphan classification:
// a between-jobs slot's registration must never be mistaken for a stray runner.
func IsEphemeralRunnerName(name string) bool {
	return strings.HasPrefix(name, EphemeralNamePrefix)
}

// Download identifies a runner application tarball and its expected checksum.
type Download struct {
	URL    string
	SHA256 string
}

// RunnerSpec describes one runner to create on the host.
type RunnerSpec struct {
	Name   string
	Org    string
	URL    string   // https://github.com/<org>
	Labels []string // custom labels (tags); defaults self-hosted/linux/x64 are added by the agent
	Group  string   // runner group NAME (must already exist); "" = default group
}

// Orchestrator manages the on-machine actions/runner agent on Ubuntu x64. Its
// methods must run as root: they create a dedicated user, install systemd
// services, and run apt via installdependencies.sh. The agent itself never runs
// as root - it is configured and run as the dedicated user.
type Orchestrator interface {
	EnsureBase(ctx context.Context) error
	CreateRunner(ctx context.Context, spec RunnerSpec, dl Download, regToken string) error
	// UpgradeRunnerAgent replaces a persistent runner's agent BINARIES in place with
	// the tarball described by dl, preserving its existing registration
	// (.runner/.credentials) and _work, then restarts the unit and self-tests that it
	// came back active. This is the same swap the actions/runner auto-updater performs,
	// so it needs no registration token, labels, or group - and rollback is just the
	// same call with the prior version's (cached) tarball. The caller MUST have
	// confirmed the runner is idle; on failure the runner is left stopped/partially
	// swapped for the caller to roll back. Refuses an unverifiable tarball (see
	// CachedAgentTarball). Persistent runners only; ephemeral lanes upgrade via
	// EnsureEphemeralSlot.
	UpgradeRunnerAgent(ctx context.Context, org, name string, dl Download) error
	// CachedAgentTarball reports whether dl's tarball is already present (and, when
	// dl.SHA256 is set, checksum-valid) in the host cache. The upgrade engine checks
	// it before a rollback so rollback never triggers a fresh, unverifiable download
	// of a version whose checksum GitHub no longer publishes.
	CachedAgentTarball(dl Download) bool
	RemoveRunner(ctx context.Context, name, org, removeToken string) error
	// AgentID reads the GitHub runner id the agent recorded locally in its .runner
	// file at registration. This host-local, host-bound id is how a runner must be
	// deregistered - never a name lookup across the org, which can resolve to
	// another host's same-named runner. Returns an error if the file is absent.
	AgentID(org, name string) (int64, error)
	// RefreshUnit re-applies the systemd drop-in (hardening + tool-cache env) to
	// an already-installed runner and restarts it, without a full recreate.
	RefreshUnit(ctx context.Context, org, name string) error

	// --- ephemeral JIT slots (a fixed lane that mints a fresh registration per job) ---

	// EnsureEphemeralSlot stands up (or replaces) one ephemeral slot lane: a warm
	// agent tree owned by the per-org user, the persisted mint params, the aggregate
	// slice, and the slot's systemd unit (enabled + started). Idempotent.
	EnsureEphemeralSlot(ctx context.Context, spec EphemeralSlotSpec, dl Download) error
	// RemoveEphemeralSlot stops + removes a slot lane's unit and deletes its tree.
	RemoveEphemeralSlot(ctx context.Context, org, slot string) error
	// EphemeralMintParams reads the slot's persisted JIT mint params (group + labels)
	// so each `srm _runner-cycle` process mints with the same identity.
	EphemeralMintParams(org, slot string) (EphemeralParams, error)
	// RunJob resets the slot's per-job workspace, drops from root to the per-org
	// user, and runs run.sh for EXACTLY one job with the given JIT config. Root-only.
	RunJob(ctx context.Context, org, slot, jitConfig string) error
	// PendingJIT returns the runner id recorded for an in-flight cycle (0 if none) -
	// a non-zero value after a crash is a ghost the caller should deregister.
	PendingJIT(org, slot string) int64
	// RecordJIT persists (fsync) the just-minted runner id BEFORE the job runs, so a
	// crash leaves a reapable id. ClearJIT removes it after a clean cycle.
	RecordJIT(org, slot string, id int64) error
	ClearJIT(org, slot string) error
	// PruneDepCache evicts build-tool cache entries not accessed within maxAge.
	PruneDepCache(ctx context.Context, maxAge time.Duration, dryRun bool) (PruneStats, error)

	// PurgeBase executes the host-base removals for `srm uninstall`: the named
	// service users (userdel; absent users are skipped), the given absolute paths
	// (recursively), and - when RemoveSlice is set - the aggregate srm.slice unit
	// plus a daemon-reload. WHAT to remove (the never-touch-foreign-infra policy)
	// is the caller's decision; this only performs the removals, idempotently,
	// aggregating failures rather than stopping at the first.
	PurgeBase(ctx context.Context, p BasePurge) error

	// --- read-only host inspection for `srm reconcile` ---

	// ListUnits returns the base names of all srm-managed PERSISTENT runner systemd
	// units on the host (including orphans whose install tree is gone). Host-global.
	// Ephemeral slot units are returned separately by ListEphemeralUnits.
	ListUnits(ctx context.Context) ([]string, error)
	// ListEphemeralUnits returns the base names of all ephemeral slot units on the
	// host (actions.ephemeral.<org>.<slot>.service). Kept SEPARATE from ListUnits so
	// reconcile never joins these JIT-churning lanes to the GitHub runner list.
	ListEphemeralUnits(ctx context.Context) ([]string, error)
	// Inspect gathers the host-side state of one runner (unit/tree presence,
	// active, User=, drop-in conformance, cgroup memory).
	Inspect(ctx context.Context, org, name string) Inspection
	// InspectEphemeral gathers host-side health for one ephemeral slot lane (unit
	// presence, active, restart count, cgroup memory). An ephemeral slot is judged
	// by host health alone - never by GitHub registration presence.
	InspectEphemeral(ctx context.Context, org, slot string) EphemeralInspection
	// HostDisk reports filesystem usage for each path (best-effort; df-based).
	HostDisk(ctx context.Context, paths []string) []DiskStat
	// DirSize returns the total size of a directory in bytes, or -1 on error.
	DirSize(ctx context.Context, path string) int64
	// SliceUsage reports the aggregate srm.slice live memory and hard cap (cgroup
	// v2 memory.current / memory.max), or (-1, -1) when the slice doesn't exist -
	// i.e. no auto-capacity mode. This is the box-wide ceiling for all runners
	// combined, the real multi-job OOM guard.
	SliceUsage(ctx context.Context) (current, max int64)
}

// Options bundles the host-layout knobs an orchestrator needs beyond installRoot.
// Passing a struct (instead of a widening positional list) lets the caller derive
// per-org values from config - the dedicated user and cache roots today, resource
// limits and per-org isolation later - while keeping NewUbuntu's signature stable.
// Zero-valued fields fall back to the package defaults.
type Options struct {
	User      string // dedicated non-login service user that owns runner dirs
	ToolCache string // RUNNER_TOOL_CACHE (AGENT_TOOLSDIRECTORY); shared root or per-org in isolated mode
	CacheRoot string // build-tool cache root (npm/pnpm/go/... caches); shared root or per-org
	// Resources are the cgroup limits emitted into the unit drop-in. Zero value =
	// no limits (opt-in), preserving the historical unbounded behavior.
	Resources config.ResourceLimits
	// Org is the organization this orchestrator serves. Only consulted in isolated
	// mode (the per-org HOME/owned subtree is {installRoot}/{Org}).
	Org string
	// Isolated turns on per-org isolation: the user owns ONLY its org subtree
	// (0700), caches are per-org, and the unit drop-in pins User=. When false the
	// orchestrator behaves exactly as the historical single-user build.
	Isolated bool
	// Slice, when set, places every runner unit in this systemd slice (Slice=) and
	// makes the orchestrator write the slice unit so its aggregate cap is enforced.
	// Empty (the default) leaves units in system.slice, byte-identical to before.
	Slice string
	// SliceMemoryMax is the aggregate memory ceiling written onto Slice (systemd
	// syntax, e.g. "75%"). It bounds ALL runners in the slice TOGETHER - the lever
	// that prevents many moderate jobs from collectively OOM'ing the host, which a
	// per-runner cap cannot. Ignored when Slice is empty.
	SliceMemoryMax string
	// SelfExe is the path to the srm binary, baked into an ephemeral slot unit's
	// ExecStart (srm _runner-cycle ...). Empty falls back to DefaultSelfExe. Only
	// consulted by renderEphemeralUnit.
	SelfExe string
	// ProtectProc, when true, adds ProtectProc=invisible to the PERSISTENT runner
	// drop-in (ephemeral units already set it unconditionally). It hides other
	// users' /proc entries, closing the residual same-host cross-unit
	// /proc/<pid>/cmdline window. Opt-in (default off) so the default drop-in stays
	// byte-identical to the historical output.
	ProtectProc bool
}

type ubuntu struct {
	installRoot string
	opts        Options
}

// NewUbuntu returns the Ubuntu orchestrator rooted at installRoot. Unset Options
// fields default to the package conventions (DefaultRunnerUser, the shared tool
// cache, and the shared build-tool cache root), so callers override only what
// differs for an org.
func NewUbuntu(installRoot string, opts Options) Orchestrator {
	if opts.User == "" {
		opts.User = config.DefaultRunnerUser
	}
	if opts.ToolCache == "" {
		opts.ToolCache = config.DefaultToolCacheRoot
	}
	if opts.CacheRoot == "" {
		opts.CacheRoot = config.DefaultCacheRoot
	}
	return &ubuntu{installRoot: installRoot, opts: opts}
}

func run(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// EnsureBase creates the dedicated user and the host directories (idempotent). It
// dispatches to the historical single-user layout or, when Options.Isolated is
// set, to the per-org layout. The default path is kept a verbatim copy of the
// pre-isolation code so single-user behavior is byte-for-byte unchanged.
func (u *ubuntu) EnsureBase(ctx context.Context) error {
	if u.opts.Isolated {
		return u.ensureBaseIsolated(ctx)
	}
	return u.ensureBaseLegacy(ctx)
}

// ensureBaseLegacy is the historical single-user behavior: one user owns the
// whole installRoot and the host-wide tool/dep caches. UNCHANGED - do not factor
// this through the isolated path; the divergence is too easy to get subtly wrong.
func (u *ubuntu) ensureBaseLegacy(ctx context.Context) error {
	if err := exec.CommandContext(ctx, "id", u.opts.User).Run(); err != nil {
		if err := run(ctx, "useradd", "--system", "--shell", "/usr/sbin/nologin", "--home-dir", u.installRoot, u.opts.User); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(u.installRoot, 0o750); err != nil {
		return err
	}
	cache := filepath.Join(u.installRoot, ".cache")
	if err := os.MkdirAll(cache, 0o750); err != nil {
		return err
	}
	// The whole tree (incl. the user's $HOME/.cache for tool/build caches) must
	// be owned by the runner user.
	if err := run(ctx, "chown", "-R", u.opts.User+":"+u.opts.User, u.installRoot); err != nil {
		return err
	}
	// Host-wide tool cache shared by all runners (RUNNER_TOOL_CACHE). One
	// download/seed serves every runner; setup-* writes new versions here too.
	if err := os.MkdirAll(u.opts.ToolCache, 0o775); err != nil {
		return err
	}
	if err := run(ctx, "chown", "-R", u.opts.User+":"+u.opts.User, u.opts.ToolCache); err != nil {
		return err
	}
	return u.ensureCacheDirs(ctx)
}

// ensureBaseIsolated creates a per-org service user that owns ONLY its org
// subtree and private caches. Isolation is by separate ownership + 0700
// directories - deliberately NO shared unix group (a shared group would be a
// cross-org read channel). Every chown is surgically scoped to this org's paths;
// nothing here ever touches the shared installRoot tree or a sibling org's files,
// so it is safe to run per-org and re-entrant. Parent dirs are created
// traversable (0755) but never chowned recursively.
func (u *ubuntu) ensureBaseIsolated(ctx context.Context) error {
	if u.opts.Org == "" {
		return fmt.Errorf("isolated EnsureBase requires an org")
	}
	user := u.opts.User
	home := filepath.Join(u.installRoot, u.opts.Org)

	// Per-org user, $HOME = its org subtree.
	if err := exec.CommandContext(ctx, "id", user).Run(); err != nil {
		if err := run(ctx, "useradd", "--system", "--shell", "/usr/sbin/nologin", "--home-dir", home, user); err != nil {
			return err
		}
	}

	// Org HOME subtree: private (0700), owner = the org user. The parent
	// installRoot must be TRAVERSABLE by every per-org user (each is "other"
	// relative to it), so force 0755 explicitly - MkdirAll leaves an existing
	// dir's mode untouched and the legacy single-user layout created it 0750,
	// which would deny traversal and fail the unit with 200/CHDIR. The parent is
	// never chowned (siblings live under it); only its mode is widened to allow
	// traversal into the per-org 0700 subdirs.
	if err := os.MkdirAll(u.installRoot, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(u.installRoot, 0o755); err != nil {
		return err
	}
	// Shared tarball cache (installRoot/.cache): the actions/runner tarball is
	// cached here for ALL orgs and written as root. It must be bootstrapped
	// independently of any org subtree so a fresh host - or a recreate after a
	// full uninstall removed installRoot - works with no manual setup. (Legacy
	// mode creates this too; the isolated path previously omitted it.)
	if err := os.MkdirAll(filepath.Join(u.installRoot, ".cache"), 0o755); err != nil {
		return err
	}
	if err := mkdirOwned(ctx, home, 0o700, user); err != nil {
		return err
	}
	if err := mkdirOwned(ctx, filepath.Join(home, ".cache"), 0o700, user); err != nil {
		return err
	}
	if err := run(ctx, "chown", "-R", user+":"+user, home); err != nil {
		return err
	}

	// Per-org dep cache: private (0700). Parent /opt/srm-cache stays a shared,
	// traversable directory; only the org subtree is owned/locked down.
	if u.opts.CacheRoot != "" {
		if err := ensureTraversable(filepath.Dir(u.opts.CacheRoot)); err != nil {
			return err
		}
		if err := mkdirOwned(ctx, u.opts.CacheRoot, 0o700, user); err != nil {
			return err
		}
		for _, e := range config.ToolCacheEnv(u.opts.CacheRoot) {
			if err := os.MkdirAll(e.Val, 0o700); err != nil {
				return fmt.Errorf("cache dir %s: %w", e.Val, err)
			}
		}
		if err := run(ctx, "chown", "-R", user+":"+user, u.opts.CacheRoot); err != nil {
			return err
		}
	}

	// Per-org tool cache: holds only public toolchains (no secrets), so 0755 is
	// fine; ownership is still per-org so each org reads+writes its own. Starts
	// cold (setup-* re-fetches); the parent is traversable but not chowned.
	if err := ensureTraversable(filepath.Dir(u.opts.ToolCache)); err != nil {
		return err
	}
	if err := mkdirOwned(ctx, u.opts.ToolCache, 0o755, user); err != nil {
		return err
	}
	return run(ctx, "chown", "-R", user+":"+user, u.opts.ToolCache)
}

// ensureTraversable makes a shared parent directory exist and be traversable by
// every per-org user (each is "other" relative to it). MkdirAll's mode is masked
// by the process umask, so the mode is forced with an explicit Chmod - on a
// hardened-umask host the parent would otherwise be created without o+x and per-
// org runners (which are "other" here) could not reach their own subdir. The
// parent is never chowned (siblings live under it); only its mode is widened.
func ensureTraversable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.Chmod(dir, 0o755)
}

// mkdirOwned creates dir (if missing), forces its mode with an explicit chmod
// (MkdirAll's mode arg is masked by the process umask, so 0700/0755 cannot be
// trusted from MkdirAll alone), and chowns just that directory to user.
func mkdirOwned(ctx context.Context, dir string, mode os.FileMode, user string) error {
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	if err := os.Chmod(dir, mode); err != nil {
		return err
	}
	return run(ctx, "chown", user+":"+user, dir)
}

// ensureCacheDirs creates the host-wide build-tool cache directories (one per
// ToolCacheEnv entry) and gives them to the runner user, so jobs can persist
// dependency caches there across runs. Idempotent.
func (u *ubuntu) ensureCacheDirs(ctx context.Context) error {
	if u.opts.CacheRoot == "" {
		return nil
	}
	for _, e := range config.ToolCacheEnv(u.opts.CacheRoot) {
		if err := os.MkdirAll(e.Val, 0o775); err != nil {
			return fmt.Errorf("cache dir %s: %w", e.Val, err)
		}
	}
	return run(ctx, "chown", "-R", u.opts.User+":"+u.opts.User, u.opts.CacheRoot)
}

// agentCacheRoot holds the downloaded actions/runner agent tarballs. It is
// deliberately a ROOT-OWNED 0700 directory OUTSIDE any runner tree: the tarball is
// extracted and run as root, so it must never live where untrusted job code can
// reach it. The historical location ({installRoot}/.cache) doubled as the runner
// user's $HOME/.cache (job-writable in single-user mode), which let job code swap a
// cached tarball that a later rollback would install as root - so the cache is host
// global here instead (one download serves every org). See ensureTarball.
const agentCacheRoot = "/var/lib/srm/agent-cache"

func (u *ubuntu) cachePath(dl Download) string {
	base := filepath.Base(dl.URL)
	if base == "" || base == "." || base == "/" {
		base = "actions-runner.tar.gz"
	}
	return filepath.Join(agentCacheRoot, base)
}

func (u *ubuntu) ensureTarball(ctx context.Context, dl Download) (string, error) {
	// Force the cache dir to root-only 0700 (MkdirAll won't downgrade an existing
	// dir's mode, so re-assert it - a host upgraded from an older srm may have a
	// looser one). This is the integrity boundary for every tarball srm extracts.
	if err := os.MkdirAll(agentCacheRoot, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(agentCacheRoot, 0o700); err != nil {
		return "", err
	}
	path := u.cachePath(dl)
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
		ok, sum := verifySHA256(path, dl.SHA256)
		if !ok {
			_ = os.Remove(path)
			return "", fmt.Errorf("sha256 mismatch for %s: got %s want %s", dl.URL, sum, dl.SHA256)
		}
	}
	return path, nil
}

func httpDownload(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	// Self-bootstrap the destination dir: install must assume nothing pre-exists
	// (e.g. {installRoot}/.cache after a full uninstall removed installRoot).
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func verifySHA256(path, want string) (bool, string) {
	f, err := os.Open(path)
	if err != nil {
		return false, ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, ""
	}
	sum := hex.EncodeToString(h.Sum(nil))
	return strings.EqualFold(sum, want), sum
}

func extractTarGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	clean := filepath.Clean(dst)
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dst, hdr.Name)
		if target != clean && !strings.HasPrefix(target, clean+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe tar path: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func (u *ubuntu) svcName(org, name string) string {
	return fmt.Sprintf("actions.runner.%s.%s.service", org, name)
}

// runnerDir is the per-runner tree, namespaced by org so several orgs can share
// one installRoot on the same host without colliding: {installRoot}/{org}/{name}.
func (u *ubuntu) runnerDir(org, name string) string {
	return filepath.Join(u.installRoot, org, name)
}

// teardownService stops, disables, and removes a runner's systemd unit so a
// subsequent install is idempotent. svc.sh install refuses to overwrite an
// existing unit, so re-creating a runner must fully remove the old one first.
// Best-effort throughout.
func (u *ubuntu) teardownService(ctx context.Context, dir, svc string) {
	if fileExists(filepath.Join(dir, "svc.sh")) {
		stop := exec.CommandContext(ctx, "./svc.sh", "stop")
		stop.Dir = dir
		_ = stop.Run()
		uninstall := exec.CommandContext(ctx, "./svc.sh", "uninstall")
		uninstall.Dir = dir
		_ = uninstall.Run()
	}
	_ = exec.CommandContext(ctx, "systemctl", "disable", "--now", svc).Run()
	_ = os.Remove(filepath.Join("/etc/systemd/system", svc))
	_ = os.RemoveAll(filepath.Join("/etc/systemd/system", svc+".d"))
	_ = exec.CommandContext(ctx, "systemctl", "daemon-reload").Run()
	// Clear any lingering failed state so the removed unit doesn't haunt
	// `systemctl --failed` (and `srm reconcile`) as a not-found/failed entry.
	_ = exec.CommandContext(ctx, "systemctl", "reset-failed", svc).Run()
}

// hardeningDropIn is a systemd drop-in layered over the stock svc.sh unit. It
// adds defense-in-depth that does NOT break typical CI: the runner already runs
// as a non-root user, and these add namespace/kernel protections. The stricter
// options (NoNewPrivileges / ProtectSystem=strict / RestrictSUIDSGID) are
// deliberately omitted - they break workflows that use sudo or apt - but are
// listed commented for hosts whose jobs never need them.
// CurrentDropInVersion and CurrentEphemeralVersion are the template generations
// srm renders today, stamped as a "# srm-dropin-vN" / "# srm-ephemeral-vN" comment
// in the unit. reconcile compares the on-disk marker against these BEFORE
// byte-equality, so a mixed-version fleet converges upward instead of thrashing: a
// host whose binary is older than an on-disk marker leaves that drop-in alone
// (authoritative-skip), and a newer binary owns the bump. Bump the integer (AND the
// matching "# srm-...-vN" line in the const below) whenever the rendered body
// intentionally changes: a changed body without a bump lets two same-marker hosts
// see each other as drift. TestRenderDropInGolden trips on any body change (so the
// bump isn't forgotten) and TestTemplateVersionMarkers pins the stamped marker to
// the const.
const (
	CurrentDropInVersion    = 1
	CurrentEphemeralVersion = 1
)

const hardeningDropIn = `[Service]
# srm-dropin-v1
# Managed by srm. Defense-in-depth hardening that is safe for general CI.
ProtectHome=true
PrivateTmp=true
ProtectControlGroups=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectClock=true
ProtectHostname=true
LockPersonality=true
RestrictRealtime=true
# Stricter, opt-in (break sudo/apt - enable only if your jobs never need them):
#   NoNewPrivileges=true
#   ProtectSystem=strict
#   ReadWritePaths=<runner dir>
#   RestrictSUIDSGID=true
`

// renderDropIn produces the exact contents of a managed runner's systemd drop-in:
// the hardening directives, AGENT_TOOLSDIRECTORY (points the runner at the shared
// host tool cache), and the build-tool cache-location env (npm/pnpm/go/... →
// host-persistent dirs). These must be in the runner's PROCESS environment (not
// just .env): the runner computes RUNNER_TOOL_CACHE from AGENT_TOOLSDIRECTORY at
// startup, and `run:` job steps inherit the process env so the package managers
// see the cache paths. Both go in the unit via Environment=.
//
// This is the single source of truth for drop-in contents: writeHardening writes
// what it returns, and reconcile will diff the on-disk drop-in against it to
// detect drift (missing cache env, wrong paths, …). Output is deterministic
// (ToolCacheEnv is ordered) so byte-equality is a valid conformance check.
func renderDropIn(opts Options) string {
	var b strings.Builder
	b.WriteString(hardeningDropIn)
	// Opt-in: hide other users' /proc from this runner (the ephemeral lanes always
	// set this). Off by default so the historical drop-in stays byte-identical.
	if opts.ProtectProc {
		b.WriteString("ProtectProc=invisible\n")
	}
	// Place the unit in the srm aggregate slice (auto-capacity mode) so all runners
	// share one host-wide memory ceiling. Emitted only when set, so the default
	// drop-in stays byte-identical.
	if opts.Slice != "" {
		b.WriteString("Slice=" + opts.Slice + "\n")
	}
	// In isolated mode the drop-in pins User= so the unit runs as the per-org
	// service user. Setting it here (not just via svc.sh install) is what lets
	// RefreshUnit migrate an existing runner's user in place - rewrite the drop-in
	// + re-chown its tree + restart, no recreate/re-register. Omitted entirely in
	// single-user mode so the default drop-in stays byte-identical.
	if opts.Isolated && opts.User != "" {
		b.WriteString("User=" + opts.User + "\n")
	}
	writeEnvAndLimits(&b, opts)
	return b.String()
}

// writeEnvAndLimits appends the [Service] directives shared by the persistent
// drop-in and the ephemeral unit: the tool-cache pointer, every build-tool cache
// env var, and the cgroup resource caps (omitted when unset - opt-in; MemorySwapMax=0
// keeps a hungry job from swap-thrashing the host). Centralizing them keeps the two
// unit kinds in lockstep - a cache var or cap added here lands in both - and the
// output deterministic, so reconcile's byte-equality drift check stays valid.
func writeEnvAndLimits(b *strings.Builder, opts Options) {
	b.WriteString("Environment=AGENT_TOOLSDIRECTORY=" + opts.ToolCache + "\n")
	for _, e := range config.ToolCacheEnv(opts.CacheRoot) {
		b.WriteString("Environment=" + e.Key + "=" + e.Val + "\n")
	}
	r := opts.Resources
	writeDirective(b, "MemoryHigh", r.MemoryHigh)
	writeDirective(b, "MemoryMax", r.MemoryMax)
	writeDirective(b, "MemorySwapMax", r.MemorySwapMax)
	writeDirective(b, "CPUWeight", r.CPUWeight)
	writeDirective(b, "TasksMax", r.TasksMax)
}

// ephemeralHardening is the [Service] hardening block for an ephemeral slot unit.
// It is STRICTER than the persistent drop-in: a baked-deps ephemeral job should
// never need to gain privileges, so NoNewPrivileges is ACTIVE (closing the
// setuid-regain hole that is sharper here because the unit's parent is root),
// ProtectProc=invisible hides other users' processes (so a job can't read another
// org's /proc/<pid>/cmdline), and LimitCORE=0 suppresses core dumps. ProcSubset is
// deliberately NOT set to pid - it would hide /proc/cpuinfo and break nproc-based
// build parallelism. NoNewPrivileges blocks privilege GAIN only; root dropping to
// the per-org user via setpriv still works.
const ephemeralHardening = `# srm-ephemeral-v1
# Managed by srm. Ephemeral-lane hardening (stricter than the persistent drop-in).
NoNewPrivileges=true
ProtectProc=invisible
ProtectHome=true
PrivateTmp=true
ProtectControlGroups=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectClock=true
ProtectHostname=true
LockPersonality=true
RestrictRealtime=true
LimitCORE=0
`

// renderEphemeralUnit produces the COMPLETE systemd unit for an ephemeral slot
// lane. Unlike the persistent path (svc.sh's unit + a drop-in), srm authors the
// whole unit here. It starts as ROOT (no User=) because each cycle mints a JIT
// config from the App key before dropping to the per-org user - that drop happens
// inside `srm _runner-cycle`, not via the unit's User=. Restart=always makes the
// lane a warm mint→run→reset loop (one job per process). StartLimitIntervalSec=0
// lets a healthy slot churn jobs fast without tripping systemd's start limiter (a
// genuinely broken cycle is caught by reconcile's NRestarts threshold instead).
// The env + cap body is shared with the persistent path via writeEnvAndLimits.
// Deterministic so a future reconcile can diff it.
func renderEphemeralUnit(opts Options, org, slot, workDir string) string {
	exe := opts.SelfExe
	if exe == "" {
		exe = DefaultSelfExe
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=srm ephemeral runner slot " + org + "/" + slot + " (managed by srm)\n")
	b.WriteString("StartLimitIntervalSec=0\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("WorkingDirectory=" + workDir + "\n")
	b.WriteString("ExecStart=" + exe + " _runner-cycle --org " + org + " --slot " + slot + "\n")
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=3\n")
	b.WriteString("KillMode=mixed\n")
	// Drain budget: on stop, KillMode=mixed SIGTERMs run.sh and this is how long a
	// running job has to finish before SIGKILL. Generous so a normal job isn't cut
	// off (an idle slot's run.sh exits immediately); tune per max job wallclock.
	b.WriteString("TimeoutStopSec=3600\n")
	b.WriteString(ephemeralHardening)
	if opts.Slice != "" {
		b.WriteString("Slice=" + opts.Slice + "\n")
	}
	writeEnvAndLimits(&b, opts)
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// writeDirective appends a `Key=Val` systemd line when val is set; an empty val
// omits the directive entirely.
func writeDirective(b *strings.Builder, key, val string) {
	if val != "" {
		b.WriteString(key + "=" + val + "\n")
	}
}

// renderSlice produces the contents of the srm aggregate slice unit: an optional
// host-wide memory ceiling plus MemorySwapMax=0 so the runners collectively can
// never drag the host into swap. Deterministic (same inputs → same bytes) so a
// future reconcile can diff it. Returns "" when no slice is configured.
func renderSlice(opts Options) string {
	if opts.Slice == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=srm runner aggregate cgroup (managed by srm)\n")
	b.WriteString("[Slice]\n")
	if opts.SliceMemoryMax != "" {
		b.WriteString("MemoryMax=" + opts.SliceMemoryMax + "\n")
		b.WriteString("MemorySwapMax=0\n")
	}
	return b.String()
}

// writeSliceFile writes the aggregate slice unit (no daemon-reload - the caller
// reloads once after the drop-in too). No-op when no slice is configured.
func (u *ubuntu) writeSliceFile() error {
	if u.opts.Slice == "" {
		return nil
	}
	path := filepath.Join("/etc/systemd/system", u.opts.Slice)
	return os.WriteFile(path, []byte(renderSlice(u.opts)), 0o644)
}

// BasePurge selects the host-base artifacts PurgeBase removes during uninstall.
type BasePurge struct {
	Users       []string // service users to remove via userdel (absent users skipped)
	Paths       []string // absolute dirs/files removed recursively (os.RemoveAll)
	RemoveSlice bool     // remove /etc/systemd/system/srm.slice + daemon-reload
}

// PurgeBase performs the host-base removals for uninstall. It tolerates missing
// users/paths (idempotent) and aggregates failures instead of stopping at the
// first, so a partially-broken host still gets maximally cleaned.
func (u *ubuntu) PurgeBase(ctx context.Context, p BasePurge) error {
	var errs []string
	for _, name := range p.Users {
		if _, err := user.Lookup(name); err != nil {
			continue // not present - nothing to remove
		}
		if out, err := exec.CommandContext(ctx, "userdel", name).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("userdel %s: %v: %s", name, err, strings.TrimSpace(string(out))))
		}
	}
	for _, path := range p.Paths {
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Sprintf("remove %s: %v", path, err))
		}
	}
	if p.RemoveSlice {
		if err := os.Remove(filepath.Join("/etc/systemd/system", AggregateSlice)); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("remove slice: %v", err))
		}
		_ = exec.CommandContext(ctx, "systemctl", "daemon-reload").Run()
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// writeHardening installs the systemd drop-in for a runner's unit (see
// renderDropIn for the contents), ensures the aggregate slice unit exists when
// configured, and reloads systemd once for both.
func (u *ubuntu) writeHardening(ctx context.Context, svc string) error {
	if err := u.writeSliceFile(); err != nil {
		return fmt.Errorf("write slice %s: %w", u.opts.Slice, err)
	}
	dropinDir := filepath.Join("/etc/systemd/system", svc+".d")
	if err := os.MkdirAll(dropinDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dropinDir, "10-hardening.conf"), []byte(renderDropIn(u.opts)), 0o644); err != nil {
		return err
	}
	return run(ctx, "systemctl", "daemon-reload")
}

// RefreshUnit re-writes a runner's systemd drop-in (picking up any change to the
// hardening directives or the tool-cache env) and restarts the service so the
// new Environment= takes effect - no reconfigure/recreate. The caller is
// responsible for skipping busy runners (restart aborts a running job).
func (u *ubuntu) RefreshUnit(ctx context.Context, org, name string) error {
	svc := u.svcName(org, name)
	if !fileExists(filepath.Join("/etc/systemd/system", svc)) {
		return fmt.Errorf("no installed unit for %s/%s on this host", org, name)
	}
	if err := u.writeHardening(ctx, svc); err != nil {
		return err
	}
	return run(ctx, "systemctl", "restart", svc)
}

// CreateRunner downloads + verifies + extracts the agent, configures it as the
// dedicated user, and installs + starts a systemd service. Idempotent: an
// existing runner of the same name is replaced.
func (u *ubuntu) CreateRunner(ctx context.Context, spec RunnerSpec, dl Download, regToken string) error {
	if err := u.EnsureBase(ctx); err != nil {
		return err
	}
	tarball, err := u.ensureTarball(ctx, dl)
	if err != nil {
		return err
	}
	orgDir := filepath.Join(u.installRoot, spec.Org)
	dir := u.runnerDir(spec.Org, spec.Name)
	svc := u.svcName(spec.Org, spec.Name)

	// Fully remove any prior install of this runner (incl. its systemd unit) so
	// the install below is idempotent - svc.sh install won't overwrite a unit.
	u.teardownService(ctx, dir, svc)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := extractTarGz(tarball, dir); err != nil {
		return fmt.Errorf("extract runner: %w", err)
	}
	// chown the org subtree (not just the runner dir) so the runner user can
	// traverse {installRoot}/{org} into its own directory.
	if err := run(ctx, "chown", "-R", u.opts.User+":"+u.opts.User, orgDir); err != nil {
		return err
	}

	if deps := filepath.Join(dir, "bin", "installdependencies.sh"); fileExists(deps) {
		if err := run(ctx, deps); err != nil {
			return fmt.Errorf("installdependencies: %w", err)
		}
	}

	// configure as the dedicated user (the agent refuses to run as root).
	// --disableupdate turns OFF the agent's built-in auto-updater: srm owns runner
	// versions (`srm runners upgrade`), so the agent must not silently self-update
	// out from under the state manifest, which would desync the version the upgrade
	// gates (skip-if-at-target, anti-downgrade) read. GitHub still schedules jobs to a
	// pinned-version runner; srm is responsible for keeping it current.
	args := []string{"-u", u.opts.User, "--", "env", "RUNNER_ALLOW_RUNASROOT=0", "./config.sh",
		"--unattended", "--replace", "--disableupdate", "--url", spec.URL, "--token", regToken,
		"--name", spec.Name, "--labels", strings.Join(spec.Labels, ","), "--work", "_work"}
	if spec.Group != "" {
		args = append(args, "--runnergroup", spec.Group)
	}
	cfg := exec.CommandContext(ctx, "runuser", args...)
	cfg.Dir = dir
	if out, err := cfg.CombinedOutput(); err != nil {
		return fmt.Errorf("config.sh: %w: %s", err, redactToken(string(out), regToken))
	}

	// install the stock systemd unit (as root), running as the dedicated user
	inst := exec.CommandContext(ctx, "./svc.sh", "install", u.opts.User)
	inst.Dir = dir
	if out, err := inst.CombinedOutput(); err != nil {
		return fmt.Errorf("svc.sh install: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// layer hardening via a systemd drop-in before starting
	if err := u.writeHardening(ctx, svc); err != nil {
		return fmt.Errorf("hardening drop-in: %w", err)
	}
	start := exec.CommandContext(ctx, "./svc.sh", "start")
	start.Dir = dir
	if out, err := start.CombinedOutput(); err != nil {
		return fmt.Errorf("svc.sh start: %w: %s", err, strings.TrimSpace(string(out)))
	}

	time.Sleep(2 * time.Second)
	if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", svc).Run(); err != nil {
		return fmt.Errorf("service %s not active after start", svc)
	}
	return nil
}

// CachedAgentTarball reports whether dl's tarball is already on disk in the host
// cache, verifying its checksum when dl.SHA256 is set. See the interface doc.
func (u *ubuntu) CachedAgentTarball(dl Download) bool {
	path := u.cachePath(dl)
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

// agentPayloadDirs are the version-specific directories the runner tarball fully
// replaces. They are snapshotted (renamed aside) before an upgrade and restored on
// failure - everything else in the tree (.runner/.credentials/_work/_diag and the
// thin top-level scripts) is registration/runtime state that survives the swap.
var agentPayloadDirs = []string{"bin", "externals"}

const snapshotSuffix = ".srm-prev"

// UpgradeRunnerAgent swaps a persistent runner's agent binaries in place, preserving
// its registration. The version-specific payload (bin/, externals/) is RENAMED aside
// as a snapshot, the new tarball is extracted clean (no stale files mixed in), and
// the unit is restarted and self-tested. On ANY failure the snapshot is renamed back
// and the runner restarted, so a failed upgrade leaves it on its prior, working
// agent - the rollback is a local rename, independent of the download cache or the
// recorded version (so it works even for a runner srm has no manifest entry for).
func (u *ubuntu) UpgradeRunnerAgent(ctx context.Context, org, name string, dl Download) error {
	svc := u.svcName(org, name)
	dir := u.runnerDir(org, name)
	if !fileExists(filepath.Join("/etc/systemd/system", svc)) {
		return fmt.Errorf("no installed unit for %s/%s on this host", org, name)
	}
	// Only upgrade a CONFIGURED runner: the swap deliberately keeps .runner, so a
	// tree without one was never registered and must go through create, not upgrade.
	if !fileExists(filepath.Join(dir, ".runner")) {
		return fmt.Errorf("%s/%s has no .runner (not configured) - create it instead of upgrading", org, name)
	}
	// Never install an unverifiable agent. A forward upgrade carries dl.SHA256 (so
	// ensureTarball downloads + verifies); an explicit rollback to an older version
	// passes an empty SHA256 but the prior tarball must already be in the root-only
	// cache, so we accept the cached copy and never reach for an unverifiable download.
	if dl.SHA256 == "" && !u.CachedAgentTarball(dl) {
		return fmt.Errorf("refusing to upgrade %s/%s to an unverifiable agent: %s has no published checksum and is not in the host cache", org, name, filepath.Base(dl.URL))
	}
	tarball, err := u.ensureTarball(ctx, dl)
	if err != nil {
		return err
	}

	// Recover from any snapshot a previously-crashed upgrade left behind, so the tree
	// is whole before we touch it.
	recoverAgentSnapshot(dir)

	// Stop BEFORE swapping so the running agent releases its binaries.
	if err := run(ctx, "systemctl", "stop", svc); err != nil {
		return fmt.Errorf("stop %s: %w", svc, err)
	}
	// Snapshot the version-specific payload aside (rename = instant, same filesystem).
	if err := snapshotAgentPayload(dir); err != nil {
		_ = run(ctx, "systemctl", "start", svc) // bring the (untouched) old agent back
		return fmt.Errorf("snapshot current agent: %w", err)
	}
	// Extract the new payload clean, chown, re-assert the drop-in, and start. Any
	// failure here (or in the self-test below) rolls the snapshot back.
	swapErr := func() error {
		if err := extractTarGz(tarball, dir); err != nil {
			return fmt.Errorf("extract runner: %w", err)
		}
		if err := run(ctx, "chown", "-R", u.opts.User+":"+u.opts.User, dir); err != nil {
			return err
		}
		if err := u.writeHardening(ctx, svc); err != nil {
			return fmt.Errorf("hardening drop-in: %w", err)
		}
		if err := run(ctx, "systemctl", "start", svc); err != nil {
			return fmt.Errorf("start %s: %w", svc, err)
		}
		// Self-test: a Type=simple unit reports active the instant it forks, before the
		// .NET agent loads and connects, so a single sample is meaningless. Require it
		// to STAY up without a restart for a window instead.
		return u.waitActive(ctx, svc)
	}()

	if swapErr != nil {
		// Roll back to the snapshotted agent and bring it up.
		_ = run(ctx, "systemctl", "stop", svc)
		restoreErr := restoreAgentPayload(dir)
		_ = run(ctx, "systemctl", "start", svc)
		if restoreErr != nil {
			return fmt.Errorf("upgrade failed (%v); rollback also failed (%v): %w", swapErr, restoreErr, ErrRunnerDown)
		}
		return fmt.Errorf("upgrade failed and was rolled back to the prior agent: %w", swapErr)
	}

	// Success: discard the snapshot.
	discardAgentSnapshot(dir)
	return nil
}

// snapshotAgentPayload renames bin/ and externals/ aside (to <name>.srm-prev) so the
// new tarball extracts onto a clean tree and the old payload can be restored.
func snapshotAgentPayload(dir string) error {
	for _, sub := range agentPayloadDirs {
		src := filepath.Join(dir, sub)
		bak := src + snapshotSuffix
		_ = os.RemoveAll(bak) // clear any stale leftover
		if fileExists(src) {
			if err := os.Rename(src, bak); err != nil {
				return err
			}
		}
	}
	return nil
}

// restoreAgentPayload removes the freshly-extracted payload and renames the snapshot
// back, undoing snapshotAgentPayload.
func restoreAgentPayload(dir string) error {
	for _, sub := range agentPayloadDirs {
		dst := filepath.Join(dir, sub)
		bak := dst + snapshotSuffix
		if fileExists(bak) {
			_ = os.RemoveAll(dst)
			if err := os.Rename(bak, dst); err != nil {
				return err
			}
		}
	}
	return nil
}

// discardAgentSnapshot removes the snapshot after a successful upgrade.
func discardAgentSnapshot(dir string) {
	for _, sub := range agentPayloadDirs {
		_ = os.RemoveAll(filepath.Join(dir, sub) + snapshotSuffix)
	}
}

// recoverAgentSnapshot makes the tree whole after an upgrade that crashed mid-swap:
// if a payload dir is missing but its snapshot exists, the snapshot is restored;
// otherwise a leftover snapshot is discarded.
func recoverAgentSnapshot(dir string) {
	for _, sub := range agentPayloadDirs {
		live := filepath.Join(dir, sub)
		bak := live + snapshotSuffix
		if !fileExists(bak) {
			continue
		}
		if !fileExists(live) {
			_ = os.Rename(bak, live)
		} else {
			_ = os.RemoveAll(bak)
		}
	}
}

// waitActive polls a unit for ~24s and fails if it restarts (crash-loop) or enters a
// failed/inactive state - a far stronger check than a single is-active sample, which
// a Type=simple unit passes the instant it forks (before the agent can fault).
func (u *ubuntu) waitActive(ctx context.Context, svc string) error {
	base := u.nRestarts(ctx, svc)
	for i := 0; i < 12; i++ {
		time.Sleep(2 * time.Second)
		if u.nRestarts(ctx, svc) > base {
			return fmt.Errorf("service %s restarted (crash-loop) after start", svc)
		}
		switch u.activeState(ctx, svc) {
		case "failed", "inactive":
			return fmt.Errorf("service %s entered a failed state after start", svc)
		}
	}
	if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", svc).Run(); err != nil {
		return fmt.Errorf("service %s not active after start", svc)
	}
	return nil
}

func (u *ubuntu) nRestarts(ctx context.Context, svc string) int {
	out, _ := exec.CommandContext(ctx, "systemctl", "show", "-p", "NRestarts", "--value", svc).Output()
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

func (u *ubuntu) activeState(ctx context.Context, svc string) string {
	out, _ := exec.CommandContext(ctx, "systemctl", "show", "-p", "ActiveState", "--value", svc).Output()
	return strings.TrimSpace(string(out))
}

// RemoveRunner stops + uninstalls the service, deregisters if a remove token is
// supplied, and deletes the on-disk tree. Best-effort: the API delete is the
// source of truth for the GitHub side.
func (u *ubuntu) RemoveRunner(ctx context.Context, name, org, removeToken string) error {
	dir := u.runnerDir(org, name)
	svc := u.svcName(org, name)
	u.teardownService(ctx, dir, svc)
	if removeToken != "" && fileExists(filepath.Join(dir, "config.sh")) {
		c := exec.CommandContext(ctx, "runuser", "-u", u.opts.User, "--", "env", "RUNNER_ALLOW_RUNASROOT=0",
			"./config.sh", "remove", "--token", removeToken)
		c.Dir = dir
		_ = c.Run()
	}
	return os.RemoveAll(dir)
}

// dotRunner mirrors the fields srm needs from the agent's .runner file. agentId is
// the GitHub runner id the REST API uses to deregister this exact runner.
type dotRunner struct {
	AgentID int64 `json:"agentId"`
}

// AgentID reads the GitHub runner id recorded in the agent's .runner file at
// registration (written by config.sh). It is the host-local, host-bound identity
// used to deregister THIS host's runner by id - never a name lookup across the org,
// which could match (and delete) another host's same-named runner.
func (u *ubuntu) AgentID(org, name string) (int64, error) {
	data, err := os.ReadFile(filepath.Join(u.runnerDir(org, name), ".runner"))
	if err != nil {
		return 0, err
	}
	var dr dotRunner
	// The agent (.NET) writes .runner as UTF-8 WITH a byte-order mark; strip it so
	// encoding/json doesn't choke on the leading BOM bytes.
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}), &dr); err != nil {
		return 0, fmt.Errorf("parse .runner: %w", err)
	}
	if dr.AgentID == 0 {
		return 0, fmt.Errorf(".runner has no agentId")
	}
	return dr.AgentID, nil
}

// EphemeralSlotSpec describes one ephemeral slot lane to stand up on the host.
type EphemeralSlotSpec struct {
	Org     string
	Slot    string
	URL     string   // https://github.com/<org> (parity with persistent; JIT mint is org-scoped)
	Labels  []string // custom labels minted into every JIT registration for this slot
	GroupID int64    // runner group id minted into every JIT registration
}

// EphemeralParams are the JIT mint parameters persisted with a slot so each cycle
// (a fresh `srm _runner-cycle` process) mints the same group + labels.
type EphemeralParams struct {
	GroupID int64    `json:"groupID"`
	Labels  []string `json:"labels"`
}

// ephemeralStateRoot holds srm's root-only control files for ephemeral slots. It
// is deliberately OUTSIDE the slot tree: the slot tree is owned by the per-org
// user (so the dropped run.sh can write _work), and untrusted job code running as
// that user must NOT be able to forge .jit-id (a cross-runner deregister DoS) or
// poison .jit-params (mint with different labels/group). These live here, root
// 0700, where the job can't reach them.
const ephemeralStateRoot = "/var/lib/srm/ephemeral"

// ephemeralSlotDir is the warm tree for one slot lane (extracted agent + _work),
// under the org subtree so per-org isolation (0700, org-owned) covers it.
func (u *ubuntu) ephemeralSlotDir(org, slot string) string {
	return filepath.Join(u.installRoot, org, ".ephemeral", slot)
}

// ephemeralControlDir is the root-owned 0700 directory holding the slot's jit-id
// and jit-params - see ephemeralStateRoot for why it is separate from the slot tree.
func (u *ubuntu) ephemeralControlDir(org, slot string) string {
	return filepath.Join(ephemeralStateRoot, org, slot)
}

func (u *ubuntu) jitIDPath(org, slot string) string {
	return filepath.Join(u.ephemeralControlDir(org, slot), "jit-id")
}

func (u *ubuntu) jitParamsPath(org, slot string) string {
	return filepath.Join(u.ephemeralControlDir(org, slot), "jit-params")
}

// EphemeralSlotFromName extracts the slot id from a minted JIT runner name
// (srm-eph-<org>-<slot>-<token>) given the known org. Using the known org avoids
// the ambiguity of org names containing dashes. Returns ok=false if name is not a
// slot registration for org.
func EphemeralSlotFromName(name, org string) (string, bool) {
	rest := strings.TrimPrefix(name, EphemeralNamePrefix+org+"-")
	if rest == name { // prefix absent
		return "", false
	}
	i := strings.IndexByte(rest, '-') // rest = <slot>-<token>; slot has no dashes
	if i <= 0 {
		return "", false
	}
	return rest[:i], true
}

// EnsureEphemeralSlot stands up (or replaces) an ephemeral slot lane. The unit
// runs `srm _runner-cycle` as ROOT, which mints a JIT config then drops to the
// per-org user for one job. Perms mirror the isolated runner layout: the org
// subtree is created/owned by EnsureBase; the slot tree is owned by the per-org
// user so the dropped run.sh can write _work. Idempotent.
func (u *ubuntu) EnsureEphemeralSlot(ctx context.Context, spec EphemeralSlotSpec, dl Download) error {
	if err := u.EnsureBase(ctx); err != nil {
		return err
	}
	tarball, err := u.ensureTarball(ctx, dl)
	if err != nil {
		return err
	}
	dir := u.ephemeralSlotDir(spec.Org, spec.Slot)
	svc := EphemeralSvcName(spec.Org, spec.Slot)

	// Fully tear down any prior lane + tree so the rebuild is idempotent.
	u.teardownEphemeral(ctx, svc)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	// The org dir, .ephemeral parent, and slot dir are owned by the per-org user so
	// the dropped run.sh can traverse + write them (mode forced via mkdirOwned -
	// MkdirAll is umask-masked). The org dir MUST be owned here in BOTH modes: in
	// isolated mode EnsureBase already made it the user's 0700 HOME, but in legacy
	// mode ensureBaseLegacy never creates {installRoot}/{org}, so MkdirAll would
	// leave it root:root and run.sh (as the dropped user) could not traverse it
	// (200/CHDIR) - exactly the perms class the persistent path chowns for.
	if err := mkdirOwned(ctx, filepath.Join(u.installRoot, spec.Org), 0o700, u.opts.User); err != nil {
		return err
	}
	if err := mkdirOwned(ctx, filepath.Join(u.installRoot, spec.Org, ".ephemeral"), 0o700, u.opts.User); err != nil {
		return err
	}
	if err := mkdirOwned(ctx, dir, 0o700, u.opts.User); err != nil {
		return err
	}
	if err := extractTarGz(tarball, dir); err != nil {
		return fmt.Errorf("extract runner: %w", err)
	}
	if deps := filepath.Join(dir, "bin", "installdependencies.sh"); fileExists(deps) {
		if err := run(ctx, deps); err != nil {
			return fmt.Errorf("installdependencies: %w", err)
		}
	}
	// The per-org user owns the whole slot tree (extracted agent + _work) so the
	// dropped run.sh can read/write it. Done BEFORE writing the root-only control
	// files below so the chown -R never touches them.
	if err := run(ctx, "chown", "-R", u.opts.User+":"+u.opts.User, dir); err != nil {
		return err
	}
	// Persist the JIT mint params in the ROOT-only control dir (NOT the slot tree -
	// the dropped job owns the slot tree and must not be able to poison them).
	ctrl := u.ephemeralControlDir(spec.Org, spec.Slot)
	if err := os.MkdirAll(ctrl, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(ctrl, 0o700); err != nil { // force mode past umask; root-owned
		return err
	}
	params, err := json.Marshal(EphemeralParams{GroupID: spec.GroupID, Labels: spec.Labels})
	if err != nil {
		return err
	}
	if err := os.WriteFile(u.jitParamsPath(spec.Org, spec.Slot), params, 0o600); err != nil {
		return err
	}
	if err := u.writeSliceFile(); err != nil {
		return fmt.Errorf("write slice: %w", err)
	}
	unit := renderEphemeralUnit(u.opts, spec.Org, spec.Slot, dir)
	if err := os.WriteFile(filepath.Join("/etc/systemd/system", svc), []byte(unit), 0o644); err != nil {
		return err
	}
	if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	return run(ctx, "systemctl", "enable", "--now", svc)
}

// teardownEphemeral stops + removes a slot lane's unit (best-effort), clearing any
// lingering failed state so it doesn't haunt `systemctl --failed` / reconcile.
func (u *ubuntu) teardownEphemeral(ctx context.Context, svc string) {
	_ = exec.CommandContext(ctx, "systemctl", "disable", "--now", svc).Run()
	_ = os.Remove(filepath.Join("/etc/systemd/system", svc))
	_ = os.RemoveAll(filepath.Join("/etc/systemd/system", svc+".d"))
	_ = exec.CommandContext(ctx, "systemctl", "daemon-reload").Run()
	_ = exec.CommandContext(ctx, "systemctl", "reset-failed", svc).Run()
}

// RemoveEphemeralSlot stops + removes a slot lane and deletes its tree and the
// root-only control dir.
func (u *ubuntu) RemoveEphemeralSlot(ctx context.Context, org, slot string) error {
	u.teardownEphemeral(ctx, EphemeralSvcName(org, slot))
	_ = os.RemoveAll(u.ephemeralControlDir(org, slot))
	return os.RemoveAll(u.ephemeralSlotDir(org, slot))
}

// EphemeralMintParams reads the slot's persisted JIT mint params.
func (u *ubuntu) EphemeralMintParams(org, slot string) (EphemeralParams, error) {
	var p EphemeralParams
	b, err := os.ReadFile(u.jitParamsPath(org, slot))
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(b, &p)
}

// PendingJIT returns the runner id recorded for an in-flight cycle, or 0.
func (u *ubuntu) PendingJIT(org, slot string) int64 {
	b, err := os.ReadFile(u.jitIDPath(org, slot))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// RecordJIT persists the just-minted runner id (fsync'd) BEFORE the job runs, so a
// crash/reboot mid-job leaves a reapable id for the next cycle.
func (u *ubuntu) RecordJIT(org, slot string, id int64) error {
	// Self-bootstrap the control dir so a cycle survives /var/lib/srm having been
	// removed (e.g. by a prior uninstall) - never crash recording the JIT id.
	if err := os.MkdirAll(filepath.Dir(u.jitIDPath(org, slot)), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(u.jitIDPath(org, slot), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
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
func (u *ubuntu) ClearJIT(org, slot string) error {
	if err := os.Remove(u.jitIDPath(org, slot)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RunJob resets the slot's per-job workspace (keeping the warm agent tree), drops
// from root to the per-org user via setpriv (PAM-free, unlike runuser - no session
// scope to fight the slice placement), and runs run.sh for EXACTLY one job. Must
// run as root.
//
// The JIT config is passed as the `--jitconfig` ARGUMENT: the canary proved
// runner 2.335.1's run.sh does NOT read it from stdin (`--jitconfig -` is taken
// literally → "Not configured"). The blob is therefore visible in this process's
// /proc/<pid>/cmdline; that is acceptable because it is single-use and short-lived,
// the ephemeral unit sets ProtectProc=invisible (hiding it from other per-org
// users' process views), and per-org isolation separates the uids. (A global
// hidepid / ProtectProc on the persistent units would close the residual
// same-host cross-unit window - tracked as a follow-on hardening.)
func (u *ubuntu) RunJob(ctx context.Context, org, slot, jitConfig string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("RunJob must run as root to drop to %s", u.opts.User)
	}
	dir := u.ephemeralSlotDir(org, slot)
	// Fresh per-job workspace: wipe the mutable layer (and any prior JIT runtime
	// creds run.sh materialized - they belong to a spent registration), keeping the
	// warm agent binaries. run.sh re-creates .runner/.credentials from the new JIT
	// config each cycle, so removing stale ones here leaves no creds at rest between
	// jobs.
	for _, sub := range []string{"_work", "_diag", ".runner", ".credentials", ".credentials_rsaparams"} {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
			return fmt.Errorf("reset %s: %w", sub, err)
		}
	}
	if err := mkdirOwned(ctx, filepath.Join(dir, "_work"), 0o700, u.opts.User); err != nil {
		return err
	}
	// Resolve the per-org user's uid/gid (pure-Go /etc/passwd read under CGO_ENABLED=0).
	usr, err := user.Lookup(u.opts.User)
	if err != nil {
		return fmt.Errorf("lookup runner user %s: %w", u.opts.User, err)
	}
	// Fail closed: the whole point of the drop is to NOT run the job as root. The
	// unit has no User= (it starts root to mint), so this is the only guard.
	if usr.Uid == "0" || usr.Gid == "0" {
		return fmt.Errorf("refusing to run job: %s resolves to uid/gid 0", u.opts.User)
	}
	// Written record of which user the job ran as, for manual audit (root-owned).
	_ = os.WriteFile(filepath.Join(dir, ".ran-as"), []byte(u.opts.User+"\n"), 0o644)

	// setpriv drops to the per-org user; the wrapping `env RUNNER_ALLOW_RUNASROOT=0`
	// re-asserts the runner's own no-root guard (defense-in-depth, matching the
	// persistent path) and is harmless once already non-root.
	cmd := exec.CommandContext(ctx, "setpriv",
		"--reuid", usr.Uid, "--regid", usr.Gid, "--init-groups", "--",
		"env", "RUNNER_ALLOW_RUNASROOT=0", "./run.sh", "--jitconfig", jitConfig)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func redactToken(s, tok string) string {
	if tok == "" {
		return s
	}
	return strings.ReplaceAll(s, tok, "***")
}
