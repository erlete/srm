//go:build !windows

package runner

// Linux host-layout paths for the runner package. The Windows analogs (derived from
// %ProgramData% / %ProgramFiles%) live in paths_windows.go.

// DefaultSelfExe is where srm is installed on the host; an ephemeral slot unit's
// ExecStart calls it. Overridable via Options.SelfExe (e.g. os.Executable()).
const DefaultSelfExe = "/usr/local/bin/srm"

// agentCacheRoot holds the downloaded actions/runner agent tarballs. It is
// deliberately a ROOT-OWNED 0700 directory OUTSIDE any runner tree: the tarball is
// extracted and run as root, so it must never live where untrusted job code can
// reach it. The historical location ({installRoot}/.cache) doubled as the runner
// user's $HOME/.cache (job-writable in single-user mode), which let job code swap a
// cached tarball that a later rollback would install as root - so the cache is host
// global here instead (one download serves every org). See ensureTarball.
const agentCacheRoot = "/var/lib/srm/agent-cache"

// ephemeralStateRoot holds srm's root-only control files for ephemeral slots. It
// is deliberately OUTSIDE the slot tree: the slot tree is owned by the per-org
// user (so the dropped run.sh can write _work), and untrusted job code running as
// that user must NOT be able to forge .jit-id (a cross-runner deregister DoS) or
// poison .jit-params (mint with different labels/group). These live here, root
// 0700, where the job can't reach them.
const ephemeralStateRoot = "/var/lib/srm/ephemeral"
