//go:build windows

package runner

import "github.com/erlete/srm/internal/config"

// Windows host-layout paths for the runner package. They mirror the Linux FHS
// locations (paths_linux.go) onto the machine-wide %ProgramData% / %ProgramFiles%
// roots resolved in config (see config.DataRoot et al.). They are vars (not consts)
// because they derive from env at runtime.

// DefaultSelfExe is the conventional installed srm path, used as a fallback when
// os.Executable() can't be resolved. Windows: %ProgramFiles%\srm\srm.exe.
var DefaultSelfExe = config.SelfExePath()

// agentCacheRoot is the root-only directory holding downloaded runner agent archives
// (analog of /var/lib/srm/agent-cache). Job code must never be able to write here.
var agentCacheRoot = config.AgentCacheDir()

// ephemeralStateRoot holds srm's per-slot JIT control files (analog of
// /var/lib/srm/ephemeral), under the machine-wide srm data dir.
var ephemeralStateRoot = config.EphemeralStateDir()
