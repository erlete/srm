//go:build windows

package config

import "path/filepath"

// Windows host-layout defaults. These mirror the Linux FHS locations
// (/opt/actions-runners, /opt/hostedtoolcache, /opt/srm-cache) onto Windows-native
// directories. They are vars (not consts) because the cache root derives from
// %ProgramData% at runtime (see paths_windows.go); the top-level roots use stable
// drive-letter conventions that match GitHub's Windows runner image.

// DefaultRunnerUser is the log-on account the runner services use and whose access
// is granted on each runner tree. On Windows the default is the built-in NETWORK
// SERVICE: a passwordless, low-privilege machine account that config.cmd
// --runasservice also installs the service under when no account is passed, so the
// granted account and the running account match. icacls is given its well-known SID
// at grant time (see runner.icaclsGrantee) because the localized display name does
// not always resolve. A "srm"-style local username would NOT exist here and icacls
// would fail with error 1332. Per-org isolation may map to other accounts.
const DefaultRunnerUser = `NT AUTHORITY\NETWORK SERVICE`

var (
	// DefaultInstallRoot is the parent dir for per-runner trees. C:\actions-runners
	// mirrors GitHub's own Windows runner convention (C:\actions-runner).
	DefaultInstallRoot = `C:\actions-runners`
	// DefaultToolCacheRoot is the host-wide tool cache (RUNNER_TOOL_CACHE /
	// AGENT_TOOLSDIRECTORY). C:\hostedtoolcache matches the GitHub-hosted Windows image.
	DefaultToolCacheRoot = `C:\hostedtoolcache`
	// DefaultCacheRoot is the host-wide build-tool cache root (npm/pnpm/go/... caches),
	// under the machine-wide srm data dir.
	DefaultCacheRoot = filepath.Join(DataRoot(), "cache")
)
