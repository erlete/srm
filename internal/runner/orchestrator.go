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
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
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

// DefaultSelfExe (where srm is installed on the host; an ephemeral slot unit's
// ExecStart calls it, overridable via Options.SelfExe), agentCacheRoot, and
// ephemeralStateRoot differ per OS and live in paths_linux.go / paths_windows.go.

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

	// RunnerLog writes a persistent runner's host log to w: on Linux the systemd
	// journal for its unit (journalctl -u), or - when opts.Source is
	// LogSourceAgent - the agent's own _diag log. On Windows (no journald) it
	// always tails _diag. With opts.Follow set (journal source) it streams new
	// lines until ctx is cancelled; a cancelled follow returns nil.
	RunnerLog(ctx context.Context, org, name string, opts LogOptions, w io.Writer) error
	// EphemeralLog writes an ephemeral slot lane's host log to w (its slot unit's
	// journal on Linux, which also holds srm's per-cycle output; the supervisor +
	// agent _diag on Windows).
	EphemeralLog(ctx context.Context, org, slot string, opts LogOptions, w io.Writer) error
	// EphemeralDiagDir returns the slot lane's agent _diag directory (per-job
	// Worker_*/Runner_* logs). The durable job-log capture reads a finished job's
	// log from here before the next cycle wipes it.
	EphemeralDiagDir(org, slot string) string

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
	// ConfigPath is the config file the running srm loaded from. It is baked into
	// the ephemeral slot unit's `srm _runner-cycle --config <path>` ExecStart so the
	// systemd-launched cycle loads the SAME config the operator created the lane with,
	// not whatever default resolution finds. This matters for any per-lane config that
	// only lives in a non-default file - notably docker.rootlessDinD: without the bake,
	// a DinD lane created against a custom --config would silently run with the daemon
	// OFF (the cycle would read the default /etc/srm/config.yaml, which lacks the docker
	// block). Empty = rely on default resolution (back-compat). Mirrors the Windows
	// supervisor's --config bake. Only consulted by renderEphemeralUnit.
	ConfigPath string
	// ProtectProc, when true, adds ProtectProc=invisible to the PERSISTENT runner
	// drop-in (ephemeral units already set it unconditionally). It hides other
	// users' /proc entries, closing the residual same-host cross-unit
	// /proc/<pid>/cmdline window. Opt-in (default off) so the default drop-in stays
	// byte-identical to the historical output.
	ProtectProc bool
	// DinD turns on the per-job rootless Docker sidecar for ephemeral slots. When
	// set: EnsureBase allocates the runner user a subuid/subgid range, the slot unit
	// uses the relaxed DinD hardening variant (rootless dockerd cannot start under
	// NoNewPrivileges / read-only cgroups), and each RunJob cycle starts a rootless
	// dockerd as the per-org user, injects DOCKER_HOST at it, and tears it down +
	// wipes its data-root on reset. Only consulted on the ephemeral lane; ignored by
	// the persistent drop-in (which never starts a daemon). Default off = no daemon,
	// byte-identical to before.
	DinD bool
	// BuildkitImage is the rootless BuildKit image the per-job builder is pre-seeded
	// with (see config.DefaultRootlessBuildkitImage). Only consulted when DinD is set;
	// NewUbuntu fills the default when empty.
	BuildkitImage string
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
	if opts.DinD && opts.BuildkitImage == "" {
		opts.BuildkitImage = config.DefaultRootlessBuildkitImage
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
		if err := u.ensureBaseIsolated(ctx); err != nil {
			return err
		}
	} else if err := u.ensureBaseLegacy(ctx); err != nil {
		return err
	}
	// Rootless DinD needs a subordinate uid/gid range for the user namespace; the
	// system user created above has none. Done after user creation, in both layout
	// modes, and only when DinD is on (the non-DinD base path is unchanged).
	if u.opts.DinD {
		if err := ensureSubIDRange(u.opts.User); err != nil {
			return fmt.Errorf("allocate subuid/subgid for %s: %w", u.opts.User, err)
		}
	}
	return nil
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

// subID range conventions for rootless DinD. A user namespace needs a contiguous
// block of subordinate uids/gids; 65536 is the de-facto default block size and
// 100000 the conventional first allocation (below it the host's own system/login
// uids live). Under isolation.perOrgUsers each org has its OWN user, so each gets a
// DISJOINT block and that disjointness is the kernel uid-mapping boundary between
// orgs (blocks must never overlap). In single-user mode all orgs share one user and
// thus one block - per-JOB clean-slate still holds (the data-root is wiped each job),
// but there is no cross-ORG uid boundary, consistent with single-user mode generally.
const (
	// SubIDCount is exported so doctor's readiness probe flags an undersized range
	// against the SAME width the allocator writes (one source of truth).
	SubIDCount = 65536
	subIDBase  = 100000
)

// ensureSubIDRange guarantees the runner user has a /etc/subuid AND /etc/subgid
// range so rootless dockerd can map a user namespace. `useradd --system` allocates
// none, so srm must. Idempotent and non-overlapping: if the user already has an
// entry in a file it is left untouched (if wide enough); otherwise a SubIDCount-wide block is
// appended just past the highest existing range (>= subIDBase), so a second org's
// user never collides with the first. Root-only (writes /etc/sub*id).
func ensureSubIDRange(user string) error {
	for _, path := range []string{"/etc/subuid", "/etc/subgid"} {
		if err := ensureSubIDFile(path, user); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

func ensureSubIDFile(path, user string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next := subIDBase
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) != 3 {
			continue
		}
		if f[0] == user {
			// Already allocated. Verify the block is wide enough: a hand-edited or
			// distro-seeded range narrower than SubIDCount cannot map the full user
			// namespace, and rootless dockerd then fails MID-JOB. Fail loud here (at
			// provision/create) rather than leave a silently-broken range.
			if cnt, err := strconv.Atoi(f[2]); err != nil || cnt < SubIDCount {
				return fmt.Errorf("existing %s range for %s is too small (%q, need a count >= %d); widen or remove that line", path, user, line, SubIDCount)
			}
			return nil // adequate - leave it untouched (idempotent)
		}
		start, e1 := strconv.Atoi(f[1])
		count, e2 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil {
			continue
		}
		if end := start + count; end > next {
			next = end
		}
	}
	body := string(data)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += fmt.Sprintf("%s:%d:%d\n", user, next, SubIDCount)
	return os.WriteFile(path, []byte(body), 0o644)
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

// agentCacheRoot (the root-only dir holding downloaded agent tarballs, OUTSIDE any
// runner tree so job code can't swap a tarball a later rollback installs as root) is
// defined per OS in paths_linux.go / paths_windows.go. See ensureTarball.

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
	CurrentDropInVersion = 1
	// CurrentEphemeralVersion is 2 since the DinD-aware generation: renderEphemeralUnit
	// now selects between the strict hardening block and a relaxed variant (rootless
	// dockerd cannot start under NoNewPrivileges / read-only cgroups). A v1 binary that
	// does not understand the DinD field treats a v2 unit as newer and authoritative-
	// skips it instead of reverting the relaxation - the mixed-fleet safety the marker
	// exists for. Non-DinD units re-render byte-identical apart from the v2 marker.
	CurrentEphemeralVersion = 2
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
const ephemeralHardening = `# srm-ephemeral-v2
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

// ephemeralHardeningDinD is the hardening block for an ephemeral slot that runs the
// per-job rootless Docker sidecar. The buildx docker-container driver runs BuildKit
// (and each RUN step) as NESTED unprivileged containers; that nesting needs to mount
// a fresh /proc, sethostname in its UTS namespace, write cgroupfs, and call the setuid
// newuidmap/newgidmap helpers. Most of the strict block blocks exactly those, so the
// DinD variant drops them (each was confirmed REQUIRED by live testing on Ubuntu 24.04
// / cgroup v2 - the failure mode is named beside each):
//   - NoNewPrivileges: neuters setuid newuidmap/newgidmap -> rootlesskit can't map the
//     subuid range, daemon never starts.
//   - ProtectControlGroups=true (read-only /sys/fs/cgroup): runc can't write the
//     container cgroup. Set false (Delegate=yes kept as a forward hint).
//   - ProtectProc / ProtectKernelTunables / ProtectKernelModules / ProtectKernelLogs /
//     ProtectHome / PrivateTmp: each makes systemd give the unit a private mount ns
//     with a LOCKED /proc, and an unprivileged nested userns cannot remount /proc over
//     a locked one ("error mounting proc to rootfs: operation not permitted").
//   - ProtectHostname: blocks the container's sethostname ("sethostname: operation
//     not permitted").
//
// Only the non-mount, non-namespace filters survive: ProtectClock, LockPersonality,
// RestrictRealtime, LimitCORE.
//
// SECURITY COST (documented, accepted): the DinD lane is materially less hardened than
// the strict ephemeral lane. Notably ProtectProc=invisible is gone, so a sibling org's
// runner user could read this lane's /proc/<pid>/cmdline (the single-use, short-lived
// JIT blob) - a small cross-org window the strict lane closes. What still isolates the
// lane: the unprivileged per-org user, the rootless user namespace, per-org subuid
// separation, the per-job data-root wipe + daemon teardown, and the slot unit's own
// cgroup caps (MemoryMax/Slice, applied by systemd as root over the whole subtree -
// per-CONTAINER caps are NOT enforced under cgroupfs, but whole-job bounding holds).
//
// Used ONLY when Options.DinD is set; non-DinD slots keep the strict block byte-for-byte.
const ephemeralHardeningDinD = `# srm-ephemeral-v2
# Managed by srm. Ephemeral-lane hardening, rootless-DinD variant: relaxed so the
# nested rootless build containers can start (mount /proc, sethostname, cgroupfs,
# newuidmap). The job still runs as the unprivileged, user-namespaced per-org user.
ProtectControlGroups=false
Delegate=yes
ProtectClock=true
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
	execStart := exe + " _runner-cycle --org " + org + " --slot " + slot
	// Bake the active config path so the systemd-launched cycle loads the same config
	// the lane was created with (e.g. a DinD lane's docker block), not the default.
	if opts.ConfigPath != "" {
		execStart += " --config " + opts.ConfigPath
	}
	b.WriteString("ExecStart=" + execStart + "\n")
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=3\n")
	b.WriteString("KillMode=mixed\n")
	// Drain budget: on stop, KillMode=mixed SIGTERMs run.sh and this is how long a
	// running job has to finish before SIGKILL. Generous so a normal job isn't cut
	// off (an idle slot's run.sh exits immediately); tune per max job wallclock.
	b.WriteString("TimeoutStopSec=3600\n")
	if opts.DinD {
		b.WriteString(ephemeralHardeningDinD)
	} else {
		b.WriteString(ephemeralHardening)
	}
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

// ephemeralStateRoot holds srm's root-only control files for ephemeral slots,
// deliberately OUTSIDE the (per-org-user-owned) slot tree so untrusted job code can't
// forge .jit-id or poison .jit-params. Defined per OS in paths_{linux,windows}.go.

// ephemeralSlotDir is the warm tree for one slot lane (extracted agent + _work),
// under the org subtree so per-org isolation (0700, org-owned) covers it.
func (u *ubuntu) ephemeralSlotDir(org, slot string) string {
	return filepath.Join(u.installRoot, org, ".ephemeral", slot)
}

// EphemeralDiagDir returns the slot lane's agent _diag directory, where the runner
// writes its per-job Worker_*/Runner_* logs. The durable job-log capture reads it
// after a job finishes and before the next cycle wipes it.
func (u *ubuntu) EphemeralDiagDir(org, slot string) string {
	return filepath.Join(u.ephemeralSlotDir(org, slot), "_diag")
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
	//
	// The wipe runs as REAL root (the unit has no User=; the drop to the per-org user
	// happens below via setpriv), so it reclaims files a job left owned by a subuid or
	// by root - e.g. a workflow "reclaim workspace" step that ran `chown` INSIDE a
	// rootless-DinD container, which maps container-root to a host subuid. That makes a
	// workflow-level reclaim step UNNECESSARY and HARMFUL under rootless DinD: srm
	// already hands each job a clean, runner-owned _work. reclaimPath forces the removal
	// if a plain RemoveAll is blocked (immutable bit, etc.).
	resetDirs := []string{"_work", "_diag", "_home", ".runner", ".credentials", ".credentials_rsaparams"}
	if u.opts.DinD {
		// The rootless daemon's data-root (pulled images, built layers, build cache)
		// lives in the slot tree. Wiping it HERE, before each job, is the authoritative
		// clean-slate: it closes the race where a SIGKILL'd cycle skips post-job teardown
		// and leaks images/cache/registry creds into the next (possibly cross-org) job.
		resetDirs = append(resetDirs, ".docker-data")
		// That same SIGKILL'd cycle also skips stopRootlessDocker, leaving fuse-overlayfs/
		// overlay mounts under .docker-data. Detach them first, else os.RemoveAll fails
		// EBUSY and the unit hot-loops on every cycle.
		unmountStaleSlotMounts(ctx, dir)
	}
	for _, sub := range resetDirs {
		if err := reclaimPath(ctx, filepath.Join(dir, sub)); err != nil {
			return fmt.Errorf("reset %s: %w", sub, err)
		}
	}
	if err := mkdirOwned(ctx, filepath.Join(dir, "_work"), 0o700, u.opts.User); err != nil {
		return err
	}
	// Fresh, private per-job HOME (see ephemeralJobEnv for why the job needs one).
	jobHome := filepath.Join(dir, "_home")
	if err := mkdirOwned(ctx, jobHome, 0o700, u.opts.User); err != nil {
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

	// Rootless DinD: bring up a per-job docker daemon as the per-org user and collect
	// the env (DOCKER_HOST, ...) the job must see to use it. Torn down on return so the
	// daemon never outlives the job (the next cycle's pre-job wipe reclaims the rest).
	var jobEnv []string
	if u.opts.DinD {
		daemon, denv, err := u.startRootlessDocker(ctx, org, slot, usr)
		if err != nil {
			return fmt.Errorf("start rootless docker: %w", err)
		}
		defer u.stopRootlessDocker(daemon)
		jobEnv = denv
	}

	// setpriv drops to the per-org user, then `env` sets the job environment. setpriv is
	// not a login, so HOME/USER/LOGNAME would otherwise be unset/inherited-root -
	// ephemeralJobEnv fixes that (jobEnv carries DinD's DOCKER_HOST et al, empty otherwise).
	argv := append([]string{"--reuid", usr.Uid, "--regid", usr.Gid, "--init-groups", "--", "env"},
		ephemeralJobEnv(u.opts.User, jobHome, jobEnv)...)
	argv = append(argv, "./run.sh", "--jitconfig", jitConfig)
	cmd := exec.CommandContext(ctx, "setpriv", argv...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// reclaimPath removes path and everything under it as REAL root. os.RemoveAll already
// reclaims subuid/root-owned files (root has DAC_OVERRIDE), covering the common case
// where a job left _work owned by a rootless-container subuid. If RemoveAll is still
// blocked (e.g. an immutable bit), it forces the removal: clear immutable attrs, rm -rf,
// then confirm the path is gone. A forced reclaim is logged to the cycle journal - it
// signals a job that dirtied the workspace in a way a plain wipe could not undo (e.g. a
// workflow "reclaim workspace" chown, which is unnecessary + harmful under rootless DinD).
func reclaimPath(ctx context.Context, path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	fmt.Fprintf(os.Stderr, "srm: forcing reclaim of %s (a prior job left files a plain wipe could not remove; a workflow 'reclaim workspace' chown is unnecessary and harmful under rootless DinD)\n", filepath.Base(path))
	_ = run(ctx, "chattr", "-R", "-f", "-i", path) // best-effort; chattr may be absent
	_ = run(ctx, "rm", "-rf", path)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("still present after forced removal")
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

// unmountStaleSlotMounts lazily unmounts any mount points at or under root (deepest
// first) that a SIGKILL'd prior cycle left behind - rootless dockerd's fuse-overlayfs/
// overlay under .docker-data. Best-effort: the pre-job wipe is the real gate, but a live
// mount makes os.RemoveAll fail EBUSY, so detach them first. Lazy (-l) so a still-busy
// mount detaches on last use. A no-op where /proc/self/mountinfo is absent (e.g. tests).
func unmountStaleSlotMounts(ctx context.Context, root string) {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return
	}
	root = filepath.Clean(root)
	prefix := root + "/"
	var mnts []string
	for _, line := range strings.Split(string(b), "\n") {
		// mountinfo fields are space-separated; the mount point is field 5 (index 4).
		// srm slot dirs are slugs with no spaces, so no octal-unescape is needed.
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		if mp := f[4]; mp == root || strings.HasPrefix(mp, prefix) {
			mnts = append(mnts, mp)
		}
	}
	sort.Slice(mnts, func(i, j int) bool { return len(mnts[i]) > len(mnts[j]) }) // deepest first
	for _, mp := range mnts {
		_ = run(ctx, "umount", "-l", mp)
	}
}

// ephemeralJobEnv is the environment a setpriv-dropped ephemeral job runs with. The
// slot unit starts as root (to mint the JIT config) and setpriv-drops per job; setpriv
// is NOT a login, so it leaves HOME unset and USER/LOGNAME as root. No normal GitHub
// runner does that, and srm's OWN persistent lanes get HOME from systemd's User=, so
// jobs on ephemeral lanes were the only ones missing it - breaking every action that
// reads $HOME (npm/.netrc auth, `git config --global`, changesets, ...). This sets them
// explicitly. HOME is a FRESH per-job dir (wiped with the rest of the slot's per-cycle
// state), NOT the user's shared passwd HOME: that keeps the ephemeral clean-slate (no
// creds at rest between jobs) and avoids colliding with the org's persistent runners,
// which share the passwd HOME. RUNNER_ALLOW_RUNASROOT=0 re-asserts the runner's own
// no-root guard; extra carries DinD's DOCKER_HOST/BUILDX_BUILDER (empty otherwise).
func ephemeralJobEnv(user, home string, extra []string) []string {
	env := []string{
		"RUNNER_ALLOW_RUNASROOT=0",
		"HOME=" + home,
		"USER=" + user,
		"LOGNAME=" + user,
	}
	return append(env, extra...)
}

// dindRunRoot is the tmpfs parent for per-slot rootless runtime dirs (the docker
// socket + exec-root). Under /run so it is wiped on reboot and never persists at
// rest; startRootlessDocker (re)creates the per-slot dir each cycle.
const dindRunRoot = "/run/srm/dind"

// pinnedPATH is the PATH handed to the rootless daemon and buildx. setpriv
// --init-groups does NOT establish a login PATH, and rootlesskit shells out to the
// setuid newuidmap/newgidmap helpers and to slirp4netns; without their dirs on PATH
// the daemon dies with "executable file not found". Mirrors Docker's documented
// non-systemd rootless start recipe.
const pinnedPATH = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

// rootlessBuilderName is the buildx builder srm pre-seeds against the rootless
// daemon. The job's BUILDX_BUILDER is pointed at it so the default docker-container
// driver uses a rootless-capable BuildKit with no workflow change.
const rootlessBuilderName = "srm-rootless"

func (u *ubuntu) dindRuntimeDir(org, slot string) string {
	return filepath.Join(dindRunRoot, org, slot)
}

// dindDataRoot is the rootless daemon's data-root (images/layers/build cache). It
// lives in the slot tree (user-owned) so the per-job wipe in RunJob can reclaim it;
// NOT under $HOME, which ProtectHome would otherwise hide from the daemon.
func (u *ubuntu) dindDataRoot(org, slot string) string {
	return filepath.Join(u.ephemeralSlotDir(org, slot), ".docker-data")
}

// dindDockerConfigDir is the per-slot docker/buildx CLIENT config dir (DOCKER_CONFIG).
// It lives UNDER the per-job _home, so the per-job reset wipes it (no buildx builder
// state or docker creds at rest between cycles). The builder pre-seed AND the job both
// point DOCKER_CONFIG here, so the named rootless builder srm creates is resolvable when
// the job's `docker buildx build` runs - without this the builder is written to one HOME
// and looked up from another, and BUILDX_BUILDER points at a non-existent builder.
func (u *ubuntu) dindDockerConfigDir(org, slot string) string {
	return filepath.Join(u.ephemeralSlotDir(org, slot), "_home", ".docker")
}

// userHome returns the per-org user's $HOME: the org subtree in isolated mode, the
// install root in single-user mode - matching ensureBase{Isolated,Legacy}.
func (u *ubuntu) userHome(org string) string {
	if u.opts.Isolated {
		return filepath.Join(u.installRoot, org)
	}
	return u.installRoot
}

// startRootlessDocker brings up a per-job rootless dockerd as the per-org user and
// returns the running daemon process plus the env vars the job must see to use it
// (DOCKER_HOST, XDG_RUNTIME_DIR, the per-slot DOCKER_CONFIG, and - when the rootless
// buildx builder seeds successfully - BUILDX_BUILDER). The daemon runs in the user's own user namespace
// (no host root, no docker group); its socket + exec-root are on tmpfs under /run and
// its data-root in the slot tree (wiped each cycle). The caller MUST stopRootlessDocker
// the returned process. Root-only (it setpriv-drops, like RunJob).
func (u *ubuntu) startRootlessDocker(ctx context.Context, org, slot string, usr *user.User) (*exec.Cmd, []string, error) {
	rt := u.dindRuntimeDir(org, slot)
	sock := filepath.Join(rt, "docker.sock")
	dataRoot := u.dindDataRoot(org, slot)
	dockerCfg := u.dindDockerConfigDir(org, slot)
	home := u.userHome(org)

	// Authoritatively clean the per-slot runtime dir each cycle. It is tmpfs and holds
	// the socket + exec-root, and is NOT under the data-root the pre-job reset clears.
	// Removing it unlinks any stale socket/pidfile a prior cycle left, so THIS cycle's
	// daemon binds a fresh socket and waitDockerSocket cannot false-ready against a
	// leftover listener.
	if err := os.RemoveAll(rt); err != nil {
		return nil, nil, err
	}
	// Parents traversable-but-not-listable (0711), mode FORCED past umask on every
	// component (MkdirAll's mode is umask-masked; a hardened umask would drop o+x and
	// break the per-org user's traversal into its own 0700 leaf).
	for _, d := range []string{"/run/srm", dindRunRoot, filepath.Dir(rt)} {
		if err := os.MkdirAll(d, 0o711); err != nil {
			return nil, nil, err
		}
		if err := os.Chmod(d, 0o711); err != nil {
			return nil, nil, err
		}
	}
	if err := mkdirOwned(ctx, rt, 0o700, u.opts.User); err != nil {
		return nil, nil, err
	}
	if err := mkdirOwned(ctx, dataRoot, 0o700, u.opts.User); err != nil {
		return nil, nil, err
	}
	// Per-slot DOCKER_CONFIG (buildx instance store + docker creds), under the wiped
	// _home so nothing persists between cycles. The job below is handed the SAME dir, so
	// the builder pre-seeded here is resolvable in-job.
	if err := mkdirOwned(ctx, dockerCfg, 0o700, u.opts.User); err != nil {
		return nil, nil, err
	}
	// Pin a daemon config that (a) shadows any host /etc/docker/daemon.json and (b)
	// forces the cgroupfs cgroup driver. The systemd driver (dockerd's default) makes
	// the daemon create each container's cgroup as a systemd scope via a user session
	// bus - which this setpriv-launched daemon does NOT have (no `systemctl --user`, no
	// linger), so container start fails ("cgroup.controllers: no such file" / "Interactive
	// authentication required"). cgroupfs sidesteps that; the build container then runs
	// without per-container limits (whole-job bounds still come from the slot unit's
	// systemd caps). Validated live on Ubuntu 24.04 / cgroup v2.
	cfgFile := filepath.Join(rt, "daemon.json")
	if err := os.WriteFile(cfgFile, []byte(`{"exec-opts":["native.cgroupdriver=cgroupfs"]}`+"\n"), 0o644); err != nil {
		return nil, nil, err
	}

	// dockerd-rootless.sh defaults its socket to $XDG_RUNTIME_DIR/docker.sock, so
	// pointing XDG_RUNTIME_DIR at the per-slot runtime dir fixes the socket path with
	// no --host needed. --data-root moves state off $HOME (hidden by ProtectHome) into
	// the wiped slot tree. setProcessGroup puts the daemon in its own group so
	// stopRootlessDocker can reap the whole rootlesskit/dockerd tree. The daemon is NOT
	// bound to ctx: a cancelled job ctx must not race the daemon down before
	// stopRootlessDocker drains it - we own its lifecycle.
	daemon := exec.Command("setpriv",
		"--reuid", usr.Uid, "--regid", usr.Gid, "--init-groups", "--",
		"env", "HOME="+home, "XDG_RUNTIME_DIR="+rt, "PATH="+pinnedPATH,
		"dockerd-rootless.sh", "--config-file", cfgFile, "--data-root", dataRoot)
	setProcessGroup(daemon)
	daemon.Stdout, daemon.Stderr = os.Stderr, os.Stderr // daemon chatter to stderr, job stdout stays clean
	if err := daemon.Start(); err != nil {
		return nil, nil, fmt.Errorf("launch dockerd-rootless: %w", err)
	}

	if err := waitDockerSocket(ctx, sock, 90*time.Second); err != nil {
		u.stopRootlessDocker(daemon)
		return nil, nil, err
	}

	// DOCKER_CONFIG is per-slot and wiped each cycle; the job is handed the same value so
	// its `docker`/`docker buildx` reads the builder srm pre-seeds below.
	env := []string{"DOCKER_HOST=unix://" + sock, "XDG_RUNTIME_DIR=" + rt, "DOCKER_CONFIG=" + dockerCfg}
	// Pre-seed a rootless-capable buildx builder and point the job's buildx at it via
	// BUILDX_BUILDER (which outranks any builder docker/setup-buildx-action creates and
	// `docker buildx use` sets), so the default docker-container driver builds against a
	// rootless BuildKit with no workflow change. Best-effort: on failure the daemon
	// still serves bare `docker`, so log and skip BUILDX_BUILDER rather than fail the job.
	if err := u.seedRootlessBuilder(ctx, org, slot, usr, home, dockerCfg, rt, sock); err != nil {
		fmt.Fprintf(os.Stderr, "srm: rootless buildx builder pre-seed failed (bare docker still works): %v\n", err)
	} else {
		env = append(env, "BUILDX_BUILDER="+rootlessBuilderName)
	}
	return daemon, env, nil
}

// userDockerCmd builds a setpriv-dropped `docker ...` invocation as the per-org user
// pointed at the rootless daemon (DOCKER_HOST). DOCKER_CONFIG pins the buildx instance
// store to the per-slot dir the JOB also reads, so the builder srm pre-seeds here is
// resolvable when the job's `docker buildx build` runs (see startRootlessDocker). Shared
// by the builder pre-seed steps.
func (u *ubuntu) userDockerCmd(ctx context.Context, usr *user.User, home, dockerConfig, rt, sock string, dockerArgs ...string) *exec.Cmd {
	argv := []string{"--reuid", usr.Uid, "--regid", usr.Gid, "--init-groups", "--",
		"env", "HOME=" + home, "DOCKER_CONFIG=" + dockerConfig, "XDG_RUNTIME_DIR=" + rt, "PATH=" + pinnedPATH, "DOCKER_HOST=unix://" + sock}
	argv = append(argv, dockerArgs...)
	cmd := exec.CommandContext(ctx, "setpriv", argv...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd
}

// buildkitdCDIDisabled is the buildkitd.toml srm pins onto the pre-seeded builder. It
// disables CDI so a host with no GPU does not log "failed to discover GPU vendor from
// CDI: no known GPU vendor found" on every builder bootstrap (cosmetic, the build works
// regardless). CDI device injection has no role in srm's rootless build sandbox. The
// flag (--buildkitd-config) and the [cdi] section need a modern buildx/buildkit;
// seedRootlessBuilder falls back to a config-less create if either is not understood.
const buildkitdCDIDisabled = "[cdi]\n  disabled = true\n"

// seedRootlessBuilder creates (idempotently) and bootstraps the rootless buildx
// builder. It runs the STANDARD BuildKit image (u.opts.BuildkitImage, default
// config.DefaultRootlessBuildkitImage) with --oci-worker-no-process-sandbox: inside
// rootless dockerd the image's own process sandbox cannot create the nested user
// namespace it wants, and the -rootless image variant double-nests and fails, so the
// sandbox is disabled instead. dockerConfig is the per-slot buildx instance store the
// JOB will also read (DOCKER_CONFIG), so the named builder is resolvable in-job.
//
// Two cycle-cost mitigations layer on top, both fail-open (worst case is the prior
// behavior): (1) the BuildKit image is loaded from a persistent cache tar so --bootstrap
// does not re-pull it from the registry every cycle (the data-root is wiped each cycle);
// (2) a buildkitd config disables CDI to kill the no-GPU discovery noise.
func (u *ubuntu) seedRootlessBuilder(ctx context.Context, org, slot string, usr *user.User, home, dockerConfig, rt, sock string) error {
	// Bound JUST the pre-seed: a cold BuildKit image pull is legitimately slow, but a
	// wedged registry must not hang the slot lane. run.sh keeps its full job wallclock
	// (TimeoutStopSec=3600); on timeout here the caller falls back to bare docker.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// Load the pre-pulled BuildKit image from the persistent cache (it survives the
	// per-cycle data-root wipe) so the --bootstrap below finds the image already present
	// and skips the registry pull. Best-effort: a miss or a corrupt tar just means
	// bootstrap pulls, exactly as before this cache existed.
	cacheTar := u.dindImageCache()
	loaded := false
	if _, err := os.Stat(cacheTar); err == nil {
		if err := u.userDockerCmd(ctx, usr, home, dockerConfig, rt, sock, "docker", "load", "-i", cacheTar).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "srm: rootless buildkit image cache load failed (will pull): %v\n", err)
		} else {
			loaded = true
		}
	}

	// Pin a buildkitd config that disables CDI. Best-effort: if it can't be written we
	// create without it (CDI noise returns, the build still works).
	bkCfg := filepath.Join(rt, "buildkitd.toml")
	if err := os.WriteFile(bkCfg, []byte(buildkitdCDIDisabled), 0o644); err != nil {
		bkCfg = ""
	}

	// Stale buildx state for this name may survive in the per-slot DOCKER_CONFIG from a
	// prior cycle; remove it first so create is idempotent rather than failing on
	// "existing instance".
	_ = u.userDockerCmd(ctx, usr, home, dockerConfig, rt, sock, "docker", "buildx", "rm", rootlessBuilderName).Run()
	err := u.createRootlessBuilder(ctx, usr, home, dockerConfig, rt, sock, bkCfg)
	if err != nil && bkCfg != "" {
		// The buildkitd config (or its --buildkitd-config flag) may be unsupported on an
		// older buildx; retry without it so a working rootless builder still seeds. The
		// CDI noise returns, but the zero-workflow-change build path is preserved.
		fmt.Fprintf(os.Stderr, "srm: builder create with buildkitd config failed, retrying without it: %v\n", err)
		_ = u.userDockerCmd(ctx, usr, home, dockerConfig, rt, sock, "docker", "buildx", "rm", rootlessBuilderName).Run()
		err = u.createRootlessBuilder(ctx, usr, home, dockerConfig, rt, sock, "")
	}
	if err != nil {
		return err
	}

	// Seed the persistent image cache for the next cycles when we pulled this cycle (first
	// run or a cache miss). Best-effort: a failure just means the next cycle pulls again.
	if !loaded {
		if err := u.seedDinDImageCache(ctx, org, slot, usr, home, dockerConfig, rt, sock, cacheTar); err != nil {
			fmt.Fprintf(os.Stderr, "srm: rootless buildkit image cache seed failed (next cycle will pull): %v\n", err)
		}
	}
	return nil
}

// createRootlessBuilder runs `docker buildx create --bootstrap` for the rootless builder,
// optionally pinning a buildkitd config file (the CDI disable). An empty buildkitdConfig
// omits the flag entirely.
//
// default-load=true is REQUIRED, not cosmetic. The docker-container driver keeps a build's
// result ONLY in the builder's BuildKit cache unless an output is named (--push/--load/
// --output); a plain `docker build -t TAG` leaves NOTHING in the daemon image store. Because
// BUILDX_BUILDER points EVERY `docker build` at this builder (including the runner's own), the
// runner's container-action build path breaks: GitHub's runner builds each Docker container
// action (e.g. appleboy/ssh-action) with `docker build -t <hash>:<tag>` and then immediately
// `docker run <hash>:<tag>` - with the image absent from the store, docker run falls back to
// PULLING <hash> from Docker Hub and dies with "pull access denied ... repository does not
// exist". default-load makes the docker-container driver implicitly --load every no-output
// build into the daemon store (matching the classic docker driver), so the run finds it.
// Builds that DO name an output (--push/--load/--output) are unaffected. Needs buildx >= 0.14.
func (u *ubuntu) createRootlessBuilder(ctx context.Context, usr *user.User, home, dockerConfig, rt, sock, buildkitdConfig string) error {
	args := []string{"docker", "buildx", "create",
		"--name", rootlessBuilderName,
		"--driver", "docker-container",
		"--driver-opt", "image=" + u.opts.BuildkitImage,
		"--driver-opt", "default-load=true",
		"--buildkitd-flags", "--oci-worker-no-process-sandbox",
	}
	if buildkitdConfig != "" {
		args = append(args, "--buildkitd-config", buildkitdConfig)
	}
	args = append(args, "--use", "--bootstrap")
	return u.userDockerCmd(ctx, usr, home, dockerConfig, rt, sock, args...).Run()
}

// seedDinDImageCache exports the just-bootstrapped BuildKit image to the persistent cache
// tar so subsequent cycles load it locally instead of re-pulling. The export goes to a
// per-slot temp path then renames into place, so a concurrent slot of the same org never
// reads a half-written tar.
func (u *ubuntu) seedDinDImageCache(ctx context.Context, org, slot string, usr *user.User, home, dockerConfig, rt, sock, cacheTar string) error {
	if err := mkdirOwned(ctx, filepath.Dir(cacheTar), 0o700, u.opts.User); err != nil {
		return err
	}
	tmp := cacheTar + "." + org + "_" + slot + ".tmp"
	if err := u.userDockerCmd(ctx, usr, home, dockerConfig, rt, sock, "docker", "save", "-o", tmp, u.opts.BuildkitImage).Run(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, cacheTar)
}

// dindImageCache is the persistent tar holding the pre-pulled BuildKit image. It lives on
// the build-tool cache root (which survives the per-cycle data-root wipe), keyed by image
// ref so a changed BuildkitImage re-seeds rather than loading a stale image.
func (u *ubuntu) dindImageCache() string {
	return filepath.Join(u.opts.CacheRoot, "srm-dind", "buildkit-"+sanitizeImageRef(u.opts.BuildkitImage)+".tar")
}

// sanitizeImageRef reduces an image ref to a safe single filename component: alphanumerics
// and . - _ survive, every other byte becomes _.
func sanitizeImageRef(ref string) string {
	var b strings.Builder
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// stopRootlessDocker terminates the per-job rootless daemon: SIGTERM, a bounded wait
// for graceful shutdown, then SIGKILL. Best-effort - the next cycle's pre-job wipe is
// the authoritative cleanup, so any residue here is reclaimed before the next job.
func (u *ubuntu) stopRootlessDocker(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	killProcessGroup(cmd, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		killProcessGroup(cmd, syscall.SIGKILL)
		<-done
	}
}

// waitDockerSocket blocks until the rootless daemon accepts connections on its unix
// socket, or timeout/ctx elapses. A socket still not listening at the deadline is a
// HARD failure: the cycle must fail loudly, never run the job against a dead socket
// (which would resurface the same EACCES-class error pointing at a missing daemon).
func waitDockerSocket(ctx context.Context, sock string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		c, err := net.DialTimeout("unix", sock, 2*time.Second)
		if err == nil {
			_ = c.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("rootless dockerd socket %s not ready after %s: %w", sock, timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
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
