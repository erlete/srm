//go:build !windows

package config

// Linux host-layout defaults. Mainstream, secure conventions: FHS /opt for runner
// trees, a dedicated non-login service user (runners never run as root), and a
// root-only key directory. The Windows analogs live in defaults_windows.go.
const (
	// DefaultRunnerUser is the dedicated non-login system user that owns runner
	// directories and runs the runner services.
	DefaultRunnerUser = "srm"
	// DefaultInstallRoot is the FHS-friendly parent dir for per-runner trees.
	DefaultInstallRoot = "/opt/actions-runners"
	// DefaultToolCacheRoot is the host-wide tool cache (RUNNER_TOOL_CACHE). All
	// runners point AGENT_TOOLSDIRECTORY here so a version one runner downloads
	// (or `srm provision` seeds) is reused by every runner - like the GitHub-
	// hosted image's /opt/hostedtoolcache.
	DefaultToolCacheRoot = "/opt/hostedtoolcache"
	// DefaultCacheRoot is the host-wide build-tool cache root. srm points each
	// package manager's cache env var (see ToolCacheEnv) at a subdir here, so jobs
	// reuse host-persistent dependency caches across runs - and across all runners
	// on the host - instead of GitHub's 10 GB/repo cache service. No workflow
	// changes needed: these are consumed by tools running in `run:` steps.
	DefaultCacheRoot = "/opt/srm-cache"
)
