// Package config loads the tool's configuration. Multiple GitHub organizations
// are supported: each org carries its own GitHub App installation and defaults.
// Operations name their org explicitly (--org, or implicitly when only one org
// is configured) - there is no hidden "active" org that a command could silently
// scope to.
//
// Auth is GitHub App only - see docs/GITHUB_APP_SETUP.md.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"github.com/erlete/srm/internal/core"
)

// OrgConfig holds everything needed to manage one organization's runners via a
// GitHub App installation.
type OrgConfig struct {
	Name string `koanf:"name" yaml:"name"` // the org login/slug, e.g. "acme"

	// GitHub App installation auth.
	AppID          int64 `koanf:"appID" yaml:"appID"`
	InstallationID int64 `koanf:"installationID" yaml:"installationID"`
	// PrivateKeyPath points at the App's .pem (kept 0600). Leave empty to
	// resolve the key from the secrets store under "app_key:<org>" instead.
	PrivateKeyPath string `koanf:"privateKeyPath" yaml:"privateKeyPath,omitempty"`

	// Runner defaults applied when creating runners for this org.
	DefaultGroupID int64    `koanf:"defaultGroupID" yaml:"defaultGroupID"`
	DefaultLabels  []string `koanf:"defaultLabels" yaml:"defaultLabels,omitempty"`

	// InstallRoot is the host directory under which per-runner folders live,
	// e.g. /opt/actions-runners/<name>.
	InstallRoot string `koanf:"installRoot" yaml:"installRoot,omitempty"`

	// RunnerUser overrides the per-org service user when isolation.perOrgUsers is
	// on (default: "srm-<slug(org)>"). Ignored in single-user mode. See Isolation.
	RunnerUser string `koanf:"runnerUser" yaml:"runnerUser,omitempty"`

	// Profiles available for this org.
	Profiles []core.RunnerProfile `koanf:"profiles" yaml:"profiles,omitempty"`

	// Resources overrides the host-level cgroup limits for this org's runners,
	// field by field (an unset field inherits the host default). See ResourceLimits.
	Resources ResourceLimits `koanf:"resources" yaml:"resources,omitempty"`
}

// ResourceLimits are systemd (cgroup v2) resource directives applied to each
// runner's unit via the drop-in, so a runaway job is bounded to its own cgroup
// instead of taking the host down. Values are passed through verbatim to systemd,
// so they use systemd syntax (e.g. "2G", "0", "infinity", "80%"). An empty field
// omits that directive - a zero ResourceLimits applies NO limits, preserving the
// historical unbounded behavior, so this whole feature is opt-in.
//
// MemorySwapMax="0" is the highest-value setting: it stops a memory-hungry job
// from dragging the host into swap-thrash (which once wedged sshd), forcing the
// kernel to OOM-kill the offending cgroup while the host stays responsive.
type ResourceLimits struct {
	MemoryHigh    string `koanf:"memoryHigh" yaml:"memoryHigh,omitempty"`       // soft cap: throttle + reclaim above this
	MemoryMax     string `koanf:"memoryMax" yaml:"memoryMax,omitempty"`         // hard cap: cgroup is OOM-killed above this
	MemorySwapMax string `koanf:"memorySwapMax" yaml:"memorySwapMax,omitempty"` // swap cap for the cgroup ("0" = no swap)
	CPUWeight     string `koanf:"cpuWeight" yaml:"cpuWeight,omitempty"`         // relative CPU share under contention (1..10000)
	TasksMax      string `koanf:"tasksMax" yaml:"tasksMax,omitempty"`           // max processes/threads in the unit
}

// merge overlays the non-empty fields of o onto r (o wins) and returns the result.
func (r ResourceLimits) merge(o ResourceLimits) ResourceLimits {
	if o.MemoryHigh != "" {
		r.MemoryHigh = o.MemoryHigh
	}
	if o.MemoryMax != "" {
		r.MemoryMax = o.MemoryMax
	}
	if o.MemorySwapMax != "" {
		r.MemorySwapMax = o.MemorySwapMax
	}
	if o.CPUWeight != "" {
		r.CPUWeight = o.CPUWeight
	}
	if o.TasksMax != "" {
		r.TasksMax = o.TasksMax
	}
	return r
}

// IsZero reports whether no limits are set (so callers can warn that jobs are
// unbounded).
func (r ResourceLimits) IsZero() bool { return r == ResourceLimits{} }

// ResourceModeAuto enables machine-relative, auto-scaling cgroup caps. See
// Config.ResourceMode and AutoResourceLimits.
const ResourceModeAuto = "auto"

// AutoSliceMemoryMax is the host-wide ceiling for ALL srm runners combined,
// enforced by a parent systemd slice (srm.slice) under "auto" mode. It is a
// systemd PERCENTAGE so the kernel evaluates it against live RAM: srm's runners
// are collectively held to this fraction of the box, leaving the remainder for
// the OS, sshd, and the runner listeners - the headroom whose absence let a
// swap-thrash once wedge sshd. Auto-scales on resize with no reconfiguration.
const AutoSliceMemoryMax = "75%"

// AutoResourceLimits is the pre-packaged, machine-relative PER-RUNNER cap set
// used when ResourceMode is "auto". The memory caps are systemd percentages, so
// the kernel evaluates them against the host's live RAM: a single runner is held
// to a quarter of the box and begins reclaiming at a fifth, and both values
// auto-scale when the host is resized - no reconfiguration. MemorySwapMax="0" is
// absolute (the anti-swap-thrash rule). CPUWeight/TasksMax are left unset (equal
// CPU share is systemd's default; a PID cap doesn't track RAM). Any field can be
// overridden per host (Resources) or per org (orgs[].resources). The aggregate
// across all runners is bounded separately by AutoSliceMemoryMax.
func AutoResourceLimits() ResourceLimits {
	return ResourceLimits{
		MemoryHigh:    "20%",
		MemoryMax:     "25%",
		MemorySwapMax: "0",
	}
}

// Isolation controls cross-org isolation on a shared host. By default (zero
// value) every org's runners run as the single RunnerUser and share the
// host-wide tool/dep caches - there is no process or data boundary between orgs.
//
// With PerOrgUsers, each org gets its own service user ("srm-<slug(org)>", or
// OrgConfig.RunnerUser) owning a private HOME ({installRoot}/{org}, 0700), a
// private dep cache (/opt/srm-cache/<org>, 0700), and a private tool cache
// (/opt/hostedtoolcache/<org>). No shared unix group is introduced: a shared
// group would itself be a cross-org read channel, so isolation is by separate
// ownership + 0700 directories, and the tool cache is per-org rather than shared
// (public toolchains, re-fetched per org - the cost of true isolation).
type Isolation struct {
	PerOrgUsers bool `koanf:"perOrgUsers" yaml:"perOrgUsers,omitempty"`
}

// IsZero reports whether isolation is entirely unset (so yaml omitempty drops the
// block and `srm init` output stays unchanged).
func (i Isolation) IsZero() bool { return i == Isolation{} }

// HardeningConfig toggles optional, security-tightening systemd directives on the
// PERSISTENT runner drop-in. Ephemeral lanes are always hardened; these knobs
// bring the longer-lived persistent units closer without breaking typical CI by
// default. Zero value leaves the persistent drop-in byte-identical to before.
type HardeningConfig struct {
	// ProtectProc adds ProtectProc=invisible to persistent runner units, hiding
	// other users' /proc entries so a job can't read another unit's
	// /proc/<pid>/cmdline (e.g. an ephemeral lane's single-use JIT blob). Safe for
	// general CI; opt-in so the default drop-in is unchanged.
	ProtectProc bool `koanf:"protectProc" yaml:"protectProc,omitempty"`
}

// DockerConfig controls whether the ephemeral lane gives jobs a Docker daemon to
// build against. The default (zero value) is unchanged: srm ships runners bare
// with no daemon, and a job that runs `docker`/buildx fails at the socket - the
// host has no root daemon the per-org runner user may touch.
//
// RootlessDinD turns on a per-JOB ROOTLESS dockerd: each ephemeral cycle starts a
// dockerd-rootless under the per-org runner user (its own user namespace, socket
// under /run/srm/dind/<org>/<slot>), srm injects DOCKER_HOST at it, and the daemon
// + its data-root are torn down and wiped on cycle reset. Jobs build images with
// NO host root and NO shared docker group, and nothing (image layers, build cache,
// registry creds) leaks between jobs. The consumer workflow is unchanged - the
// default buildx docker-container driver connects to the injected DOCKER_HOST.
//
// It requires host prerequisites srm does not install by default (rootless docker
// binaries, uidmap, slirp4netns, fuse-overlayfs, unprivileged-userns sysctls) plus
// a per-user subuid/subgid range (allocated by EnsureBase). `srm doctor` probes
// these so a misprovisioned host fails loudly, not mid-job.
type DockerConfig struct {
	// RootlessDinD enables the per-job rootless dockerd sidecar on ephemeral slots.
	RootlessDinD bool `koanf:"rootlessDinD" yaml:"rootlessDinD,omitempty"`
	// BuildkitImage overrides the BuildKit image srm pre-seeds the buildx builder
	// with so the default docker-container driver works against the rootless daemon
	// with no workflow change. Use the STANDARD image (NOT the "-rootless" tag): the
	// builder runs as a container INSIDE the already-rootless daemon, so the -rootless
	// image's own rootlesskit double-nests and fails to write a uid_map; the standard
	// image plus --oci-worker-no-process-sandbox (srm passes it) is the working combo
	// (validated live). Empty uses DefaultRootlessBuildkitImage. Ignored when off.
	BuildkitImage string `koanf:"buildkitImage" yaml:"buildkitImage,omitempty"`
}

// DefaultRootlessBuildkitImage is the BuildKit image srm pins the pre-seeded buildx
// builder to when RootlessDinD is on and no override is set. The STANDARD image (not
// -rootless): inside a rootless daemon the -rootless image double-nests userns and
// fails; the standard image + --oci-worker-no-process-sandbox builds correctly.
const DefaultRootlessBuildkitImage = "moby/buildkit:buildx-stable-1"

// slug normalizes an org name into a valid lowercase Linux username component
// (lowercase letters, digits, '-'). It is used ONLY to mint the per-org service
// username ("srm-<slug>"); on-disk paths and systemd unit names keep the verbatim
// org name, so enabling isolation never moves an existing tree or renames a unit.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// RunnerUserFor returns the service user for an org. In single-user mode (the
// default) it is the shared RunnerUser verbatim, identical for every org. With
// per-org isolation it is OrgConfig.RunnerUser if set, else "srm-<slug(org)>".
func (c *Config) RunnerUserFor(org string) string {
	if !c.Isolation.PerOrgUsers {
		return c.RunnerUser
	}
	if oc, ok := c.Org(org); ok && oc.RunnerUser != "" {
		return oc.RunnerUser
	}
	return "srm-" + slug(org)
}

// CacheRootFor returns the build-tool dep-cache root for an org. In single-user
// mode it returns "" so the orchestrator falls back to the shared
// DefaultCacheRoot (preserving today's behavior byte-for-byte); with per-org
// isolation it is DefaultCacheRoot/<org> (private, 0700).
func (c *Config) CacheRootFor(org string) string {
	if !c.Isolation.PerOrgUsers || org == "" {
		return ""
	}
	return filepath.Join(DefaultCacheRoot, org)
}

// ToolCacheFor returns the tool-cache root (RUNNER_TOOL_CACHE) for an org. In
// single-user mode it returns "" so the orchestrator uses the shared
// DefaultToolCacheRoot; with per-org isolation it is DefaultToolCacheRoot/<org>
// (a shared group would be a cross-org read channel, so the tool cache is
// per-org too).
func (c *Config) ToolCacheFor(org string) string {
	if !c.Isolation.PerOrgUsers || org == "" {
		return ""
	}
	return filepath.Join(DefaultToolCacheRoot, org)
}

// Config is the fully-resolved tool configuration.
type Config struct {
	// SchemaVersion is the config-schema generation this file was written for.
	// Load stamps it (migrating an older/unstamped file forward) and refuses a file
	// from a NEWER srm - without it koanf would silently drop keys this binary
	// doesn't know, discarding an operator's configuration. 0/absent = a
	// pre-versioning (legacy) file, adopted as v1 on load. See CurrentSchemaVersion.
	SchemaVersion int `koanf:"schemaVersion" yaml:"schemaVersion,omitempty"`

	Orgs          []OrgConfig `koanf:"orgs" yaml:"orgs"`
	DryRun        bool        `koanf:"dryRun" yaml:"dryRun"`
	Concurrency   int         `koanf:"concurrency" yaml:"concurrency"`     // bulk-op fan-out, kept < 100
	RunnerVersion string      `koanf:"runnerVersion" yaml:"runnerVersion"` // informational; create installs the GitHub-published agent (see below)
	RunnerUser    string      `koanf:"runnerUser" yaml:"runnerUser"`       // dedicated non-login service user

	// RunnerVersionPin is the explicit target for `srm runners upgrade` when no
	// --to-version is given. Empty (the default) means "no pin": upgrade tracks the
	// version GitHub currently publishes.
	//
	// It is a SEPARATE field from RunnerVersion. NOTE: create-time does NOT consult
	// either field - CreateRunners installs whatever GitHub currently publishes
	// (linuxDownload), which is the only version with a resolvable checksum. Unlike
	// RunnerVersion, RunnerVersionPin is NEVER coerced by Load (Load fills an empty
	// RunnerVersion with DefaultRunnerVersion, so RunnerVersion can never read as
	// "unset" and so can never signal an unpinned fleet); RunnerVersionPin stays empty
	// unless the operator writes it, so empty unambiguously means unpinned.
	RunnerVersionPin string `koanf:"runnerVersionPin" yaml:"runnerVersionPin,omitempty"`

	LogFile string `koanf:"logFile" yaml:"logFile,omitempty"`
	// CacheRetentionDays is the default age cutoff for `srm cache prune` (delete
	// build-tool cache entries not accessed in this many days). 0 = use the
	// command's built-in default. Lets a cron'd prune be config-driven.
	CacheRetentionDays int `koanf:"cacheRetentionDays" yaml:"cacheRetentionDays,omitempty"`

	// JobLogRetentionDays is the age cutoff for pruning captured ephemeral job logs
	// (/var/lib/srm/joblogs) during `srm cache prune`. 0 = fall back to the prune
	// command's effective cache retention. Lets the durable job-log store be bounded
	// by the same cron'd sweep that bounds the dep cache.
	JobLogRetentionDays int `koanf:"jobLogRetentionDays" yaml:"jobLogRetentionDays,omitempty"`

	// Resources is the host-wide default cgroup limit set applied to every
	// runner's systemd unit (per-org overrides via OrgConfig.Resources). Empty =
	// no limits (opt-in). See ResourceLimits.
	Resources ResourceLimits `koanf:"resources" yaml:"resources,omitempty"`

	// ResourceMode selects how the cgroup limits are derived. "" (the default)
	// uses the literal Resources fields verbatim (empty = no limits, byte-identical
	// to the historical behavior). "auto" (ResourceModeAuto) starts from the
	// machine-relative percentage caps in AutoResourceLimits - which systemd scales
	// against live RAM, so a host that gains memory/cores needs NO reconfiguration -
	// and still lets any explicit Resources / orgs[].resources field override a
	// single value. In auto mode srm also writes a parent srm.slice capped at
	// AutoSliceMemoryMax so the runners' AGGREGATE memory is bounded. See ResourcesFor.
	ResourceMode string `koanf:"resourceMode" yaml:"resourceMode,omitempty"`

	// SliceMemoryMax overrides the aggregate memory ceiling applied to the shared
	// srm.slice in "auto" mode (systemd syntax, e.g. "80%"). Empty uses the
	// pre-packaged AutoSliceMemoryMax. This is the box-wide ceiling for ALL runners
	// COMBINED - the lever that actually prevents the host OOM a per-runner cap
	// can't. Ignored when ResourceMode is not "auto". See SliceMemoryMaxOrDefault.
	SliceMemoryMax string `koanf:"sliceMemoryMax" yaml:"sliceMemoryMax,omitempty"`

	// Isolation controls cross-org host isolation. Zero value (the default) =
	// every org shares the single RunnerUser and the host-wide caches, exactly as
	// before. See Isolation.
	Isolation Isolation `koanf:"isolation" yaml:"isolation,omitempty"`

	// Hardening toggles optional security directives on the PERSISTENT runner
	// drop-in. Zero value (default) keeps the drop-in byte-identical. See
	// HardeningConfig.
	Hardening HardeningConfig `koanf:"hardening" yaml:"hardening,omitempty"`

	// Docker controls whether ephemeral jobs get a (rootless) Docker daemon to
	// build against. Zero value (default) = no daemon, byte-identical to before.
	// See DockerConfig.
	Docker DockerConfig `koanf:"docker" yaml:"docker,omitempty"`

	// Host is the host-once dependency manifest applied by `srm provision`
	// (apt packages, setup scripts, persistent cache paths). Self-hosted runners
	// ship bare, so the job toolchain (Node, Python, …) lives here.
	Host core.DependencyManifest `koanf:"host" yaml:"host,omitempty"`
}

// DefaultRunnerVersion is the pinned actions/runner release installed unless overridden.
// It is OS-agnostic; the host-layout roots and the runner user differ per OS and live
// in defaults_linux.go / defaults_windows.go.
const DefaultRunnerVersion = "2.335.1"

// EnvVar is a name/value environment entry written into a runner's systemd unit.
type EnvVar struct{ Key, Val string }

// ToolCacheEnv returns the build-tool cache-location env vars pointing each
// package manager at a host-persistent directory under root. Injected into every
// runner's systemd drop-in (process env) so the tools - which run in `run:`
// steps and read these from the environment - keep their caches on the host
// rather than round-tripping GitHub's capped cache. Order is stable so the
// generated drop-in is deterministic. Paths are POSIX (the host is Ubuntu x64).
func ToolCacheEnv(root string) []EnvVar {
	return []EnvVar{
		{"npm_config_cache", root + "/npm"},      // npm
		{"npm_config_store_dir", root + "/pnpm"}, // pnpm content-addressable store
		{"YARN_CACHE_FOLDER", root + "/yarn"},    // yarn (classic)
		{"PIP_CACHE_DIR", root + "/pip"},         // pip
		{"GOMODCACHE", root + "/go/mod"},         // go module cache
		{"GOCACHE", root + "/go/build"},          // go build cache
		{"CARGO_HOME", root + "/cargo"},          // cargo registry + git caches
		{"GRADLE_USER_HOME", root + "/gradle"},   // gradle
	}
}

func defaults() *Config {
	return &Config{
		Concurrency:   8,
		RunnerVersion: DefaultRunnerVersion,
		RunnerUser:    DefaultRunnerUser,
		// SchemaVersion is intentionally left 0 so a loaded file's absent/legacy
		// value stays 0 and migrate() can adopt it; a fresh (missing-file) config is
		// likewise stamped to current by migrate().
	}
}

// CurrentSchemaVersion is the config-schema generation this srm writes and
// understands. Bump it whenever the on-disk config shape changes in a way an older
// srm couldn't read, and register a migration FROM the previous version in
// configMigrations. This is the control-plane half of progressive updates: it lets
// a newer srm migrate an older config forward and refuse a config from a newer srm.
const CurrentSchemaVersion = 1

// configMigrations upgrades a Config in place FROM the keyed schema version to the
// next. Every step in [0, CurrentSchemaVersion) MUST have an entry - migrate()
// errors loudly otherwise, so a forgotten registration after a version bump is
// caught by TestSchemaMigrationChainComplete rather than at a real load. Each
// migration is a pure, idempotent in-memory transform.
var configMigrations = map[int]func(*Config){
	// 0 (pre-versioning / unstamped) -> 1: the v1.0/v1.1 config shape already IS
	// schema v1, so adoption is a structural no-op - migrate() just stamps it.
	0: func(*Config) {},
}

// migrate brings a just-loaded config up to CurrentSchemaVersion, or refuses it. A
// config from a NEWER srm (schemaVersion > current) is rejected rather than loaded:
// koanf silently drops unknown keys, so loading it would discard configuration the
// operator wrote - and a later SaveConfig would persist the lossy copy.
func (c *Config) migrate() error {
	if c.SchemaVersion > CurrentSchemaVersion {
		return fmt.Errorf("config schemaVersion %d is newer than this srm understands (%d) - upgrade srm; refusing to load so unknown keys aren't silently dropped", c.SchemaVersion, CurrentSchemaVersion)
	}
	for c.SchemaVersion < CurrentSchemaVersion {
		m, ok := configMigrations[c.SchemaVersion]
		if !ok {
			return fmt.Errorf("no config migration registered from schemaVersion %d (srm bug)", c.SchemaVersion)
		}
		m(c)
		c.SchemaVersion++
	}
	return nil
}

// Load reads and validates configuration from a YAML file. A missing file is
// not an error: defaults are returned so first-run flows (srm init) can proceed.
func Load(path string) (*Config, error) {
	cfg := defaults()

	if _, err := os.Stat(path); err == nil {
		k := koanf.New(".")
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
		if err := k.Unmarshal("", cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat config %s: %w", path, err)
	}

	// Bring the schema up to date (or refuse a too-new file) before interpreting
	// any other field.
	if err := cfg.migrate(); err != nil {
		return nil, err
	}

	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 8
	}
	if cfg.RunnerVersion == "" {
		cfg.RunnerVersion = DefaultRunnerVersion
	}
	if cfg.RunnerUser == "" {
		cfg.RunnerUser = DefaultRunnerUser
	}
	if cfg.ResourceMode != "" && cfg.ResourceMode != ResourceModeAuto {
		return nil, fmt.Errorf("resourceMode %q is invalid - use %q or omit it (a typo would silently leave jobs UNBOUNDED)", cfg.ResourceMode, ResourceModeAuto)
	}
	if err := cfg.validateIsolation(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validateIsolation guards the per-org-isolation security boundary at load time.
// When perOrgUsers is on, every org must resolve to a VALID and UNIQUE service
// user: two orgs sharing a user would silently share a uid and HOME/cache
// ownership, defeating isolation (e.g. "my-org" and "my_org" both slug to
// "srm-my-org"). A no-op in single-user mode.
func (c *Config) validateIsolation() error {
	if !c.Isolation.PerOrgUsers {
		return nil
	}
	userToOrg := make(map[string]string, len(c.Orgs))
	for _, oc := range c.Orgs {
		u := c.RunnerUserFor(oc.Name)
		if !validUserName(u) {
			return fmt.Errorf("isolation: org %q resolves to invalid service user %q - set orgs[].runnerUser", oc.Name, u)
		}
		if prev, ok := userToOrg[u]; ok {
			return fmt.Errorf("isolation: orgs %q and %q both map to service user %q - set a distinct orgs[].runnerUser for one", prev, oc.Name, u)
		}
		userToOrg[u] = oc.Name
	}
	return nil
}

// validUserName reports whether s is a usable Linux service-user name: non-empty,
// no trailing dash (catches an empty slug → "srm-"), first char a letter or '_',
// and only [A-Za-z0-9_-] throughout.
func validUserName(s string) bool {
	if s == "" || strings.HasSuffix(s, "-") {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			// always allowed
		case (r >= '0' && r <= '9') || r == '-':
			if i == 0 {
				return false // must start with a letter or '_'
			}
		default:
			return false
		}
	}
	return true
}

// Org returns the configuration for a named org.
func (c *Config) Org(name string) (*OrgConfig, bool) {
	for i := range c.Orgs {
		if c.Orgs[i].Name == name {
			return &c.Orgs[i], true
		}
	}
	return nil, false
}

// ResourcesFor returns the effective cgroup limits for an org. In "auto" mode it
// starts from the machine-relative AutoResourceLimits; otherwise from the literal
// host Resources. Explicit host fields, then per-org fields, are layered on top
// (field by field, later wins), so a single value can be overridden without
// restating the rest. An unknown or empty org yields the host-level result, which
// is what host-wide callers want.
func (c *Config) ResourcesFor(org string) ResourceLimits {
	var r ResourceLimits
	if c.ResourceMode == ResourceModeAuto {
		r = AutoResourceLimits().merge(c.Resources)
	} else {
		r = c.Resources
	}
	if oc, ok := c.Org(org); ok {
		r = r.merge(oc.Resources)
	}
	return r
}

// SliceMemoryMaxOrDefault returns the aggregate srm.slice memory ceiling for
// "auto" mode: the explicit SliceMemoryMax override if set, else the pre-packaged
// AutoSliceMemoryMax. Callers apply it only in auto mode (where the slice exists).
func (c *Config) SliceMemoryMaxOrDefault() string {
	if c.SliceMemoryMax != "" {
		return c.SliceMemoryMax
	}
	return AutoSliceMemoryMax
}

// OrgNames lists the configured org slugs in order.
func (c *Config) OrgNames() []string {
	names := make([]string, len(c.Orgs))
	for i := range c.Orgs {
		names[i] = c.Orgs[i].Name
	}
	return names
}
