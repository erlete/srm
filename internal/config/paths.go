package config

import (
	"os"
	"path/filepath"
)

// This file is the Windows port's path strategy. The Linux tree uses FHS locations
// (/opt, /etc, /var/lib); the Windows port mirrors the SAME concepts onto Windows-
// native, machine-wide locations so a system service and the CLI agree. It is the
// single source of truth for the data/state roots; the host orchestrator and state
// manifest derive their paths from here.
//
// These funcs are UNTAGGED (compile on both OSes) because the cross-OS Windows
// orchestrator files reference config.DataRoot() while themselves compiling on Linux
// for CI's GOOS=linux gate; on Linux they are dead code (the Linux build uses the FHS
// literals in defaults_linux.go and in the runner/cli/state packages).
// programData()/programFiles() fall back to the C:\ literals off Windows.

// programData returns the machine-wide data root (%ProgramData%, normally
// C:\ProgramData), the Windows equivalent of /etc + /var/lib for a system service.
// Falls back to the conventional literal when the env var is somehow unset.
func programData() string {
	if p := os.Getenv("ProgramData"); p != "" {
		return p
	}
	return `C:\ProgramData`
}

// programFiles returns %ProgramFiles% (normally C:\Program Files), where the srm
// binary itself is installed.
func programFiles() string {
	if p := os.Getenv("ProgramFiles"); p != "" {
		return p
	}
	return `C:\Program Files`
}

// DataRoot is srm's machine-wide data directory: config.yaml, secrets.age, the state
// manifest, the agent-tarball cache, and ephemeral control files all live under it.
// It is the Windows analog of /etc/srm + /var/lib/srm combined.
func DataRoot() string { return filepath.Join(programData(), "srm") }

// SystemConfigPath is the machine-wide config location (analog of /etc/srm/config.yaml).
func SystemConfigPath() string { return filepath.Join(DataRoot(), "config.yaml") }

// AgentCacheDir is the root-only directory holding downloaded runner agent archives
// (analog of /var/lib/srm/agent-cache). Job code must never be able to write here.
func AgentCacheDir() string { return filepath.Join(DataRoot(), "agent-cache") }

// EphemeralStateDir holds per-slot JIT control files (analog of /var/lib/srm/ephemeral).
func EphemeralStateDir() string { return filepath.Join(DataRoot(), "ephemeral") }

// StateManifestPath is the host state manifest (analog of /var/lib/srm/state.json).
func StateManifestPath() string { return filepath.Join(DataRoot(), "state.json") }

// SelfExePath is the conventional installed location of the srm binary, used only as
// a fallback when os.Executable() can't be resolved (analog of /usr/local/bin/srm).
func SelfExePath() string { return filepath.Join(programFiles(), "srm", "srm.exe") }
