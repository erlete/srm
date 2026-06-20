package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Inspection is the host-side state of one runner, gathered for `srm reconcile`.
// All fields are best-effort: a missing systemctl/df just leaves zero values.
type Inspection struct {
	UnitExists   bool   // a systemd unit file exists for this runner
	Active       bool   // the unit is active (running)
	User         string // the unit's effective User= ("" = root/unset)
	HasTree      bool   // the install tree (config.sh) is present at the namespaced path
	HasFlatTree  bool   // an install tree exists at the legacy flat path {installRoot}/{name}
	DropInOK     bool   // the on-disk drop-in matches the expected (renderDropIn) content
	MemPeakBytes int64  // cgroup memory peak, or -1 if unknown
	MemMaxBytes  int64  // cgroup memory hard cap, or -1 = unlimited/unknown
	MemCurBytes  int64  // cgroup live memory (MemoryCurrent), or -1 if unknown
	OOMKills     int64  // cgroup memory.events oom_kill count over the unit's life, or -1
}

// EphemeralInspection is the host-side health of one ephemeral slot lane. An
// ephemeral slot is judged by host health ALONE (is the lane up and not
// crash-looping) - never by whether a JIT registration currently exists on
// GitHub, because that churns every job.
type EphemeralInspection struct {
	UnitExists   bool  // a systemd unit file exists for this slot
	Active       bool  // the lane is active (between or during a job)
	Restarts     int   // systemd NRestarts; a high count means the cycle is crash-looping
	UnitOK       bool  // the on-disk unit matches the expected (renderEphemeralUnit) content
	MemPeakBytes int64 // cgroup memory peak, or -1 if unknown
	MemMaxBytes  int64 // cgroup memory hard cap, or -1 = unlimited/unknown
	MemCurBytes  int64 // cgroup live memory (MemoryCurrent), or -1 if unknown
	OOMKills     int64 // cgroup memory.events oom_kill count over the lane's life, or -1
}

// DiskStat is a filesystem usage snapshot for one path.
type DiskStat struct {
	Path       string
	TotalBytes int64
	UsedBytes  int64
	AvailBytes int64
	UsePct     int
}

// ListUnits returns the base names of every srm-managed runner systemd unit on
// the host (actions.runner.<org>.<name>.service), orphans included. Host-global -
// the receiver's installRoot is irrelevant.
func (u *ubuntu) ListUnits(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir("/etc/systemd/system")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasPrefix(n, "actions.runner.") && strings.HasSuffix(n, ".service") {
			out = append(out, n)
		}
	}
	return out, nil
}

// ListEphemeralUnits returns the base names of every ephemeral slot unit on the
// host (actions.ephemeral.<org>.<slot>.service). Kept separate from ListUnits so
// reconcile never joins these churning, JIT-registered lanes to the GitHub runner
// list - their registration name changes every job, so a 1:1 join is impossible.
func (u *ubuntu) ListEphemeralUnits(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir("/etc/systemd/system")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasPrefix(n, EphemeralUnitPrefix) && strings.HasSuffix(n, ".service") {
			out = append(out, n)
		}
	}
	return out, nil
}

// InspectEphemeral gathers host-side health for one ephemeral slot lane: unit
// presence, active state, restart count (NRestarts - a crash-looping cycle shows
// a climbing count), and cgroup memory. Deliberately does NOT consult GitHub.
func (u *ubuntu) InspectEphemeral(ctx context.Context, org, slot string) EphemeralInspection {
	svc := EphemeralSvcName(org, slot)
	insp := EphemeralInspection{MemPeakBytes: -1, MemMaxBytes: -1, MemCurBytes: -1, OOMKills: -1}
	insp.UnitExists = fileExists(filepath.Join("/etc/systemd/system", svc))
	insp.Active = exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", svc).Run() == nil
	if n, err := strconv.Atoi(systemctlValue(ctx, svc, "NRestarts")); err == nil {
		insp.Restarts = n
	}
	// Drop-in conformance: the on-disk unit vs what we would write now (cache env,
	// caps, hardening, ExecStart). A mismatch is drift to report (e.g. caps changed).
	if b, err := os.ReadFile(filepath.Join("/etc/systemd/system", svc)); err == nil {
		insp.UnitOK = string(b) == renderEphemeralUnit(u.opts, org, slot, u.ephemeralSlotDir(org, slot))
	}
	insp.MemPeakBytes = parseBytes(systemctlValue(ctx, svc, "MemoryPeak"))
	insp.MemMaxBytes = parseBytes(systemctlValue(ctx, svc, "MemoryMax"))
	insp.MemCurBytes = parseBytes(systemctlValue(ctx, svc, "MemoryCurrent"))
	insp.OOMKills = u.oomKills(ctx, svc)
	return insp
}

// Inspect gathers the host-side state of one runner. The drop-in conformance
// check compares the on-disk drop-in against what renderDropIn would produce for
// THIS orchestrator's options - so it flags missing cache env, missing resource
// limits, or a wrong/absent User= (the conformance check for features 1-3).
func (u *ubuntu) Inspect(ctx context.Context, org, name string) Inspection {
	svc := u.svcName(org, name)
	insp := Inspection{MemPeakBytes: -1, MemMaxBytes: -1, MemCurBytes: -1, OOMKills: -1}
	insp.UnitExists = fileExists(filepath.Join("/etc/systemd/system", svc))
	insp.Active = exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", svc).Run() == nil
	insp.User = systemctlValue(ctx, svc, "User")
	insp.HasTree = fileExists(filepath.Join(u.runnerDir(org, name), "config.sh"))
	insp.HasFlatTree = fileExists(filepath.Join(u.installRoot, name, "config.sh"))
	if cur, ok := u.currentDropIn(svc); ok {
		insp.DropInOK = cur == renderDropIn(u.opts)
	}
	insp.MemPeakBytes = parseBytes(systemctlValue(ctx, svc, "MemoryPeak"))
	insp.MemMaxBytes = parseBytes(systemctlValue(ctx, svc, "MemoryMax"))
	insp.MemCurBytes = parseBytes(systemctlValue(ctx, svc, "MemoryCurrent"))
	insp.OOMKills = u.oomKills(ctx, svc)
	return insp
}

// oomKills returns how many times the kernel OOM-killed a process in this unit's
// cgroup over its lifetime (cgroup v2 memory.events oom_kill), resolving the
// cgroup path from systemd. -1 if unavailable. This is DIRECT per-runner OOM
// attribution - the smoking gun a peak-vs-cap comparison can only hint at.
func (u *ubuntu) oomKills(ctx context.Context, svc string) int64 {
	cg := systemctlValue(ctx, svc, "ControlGroup")
	if cg == "" {
		return -1
	}
	return readOOMKill(filepath.Join("/sys/fs/cgroup", cg, "memory.events"))
}

// readOOMKill parses the oom_kill counter from a cgroup v2 memory.events file
// (one "key value" pair per line). -1 if the file is missing/unreadable.
func readOOMKill(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "oom_kill" {
			if n, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				return n
			}
		}
	}
	return -1
}

// SliceUsage reports the live memory and hard cap of the srm aggregate slice
// (cgroup v2), or (-1, -1) when the slice cgroup doesn't exist (no auto-capacity
// mode). The slice bounds ALL runners together - the real multi-job OOM guard.
func (u *ubuntu) SliceUsage(_ context.Context) (current, max int64) {
	base := filepath.Join("/sys/fs/cgroup", AggregateSlice)
	return readCgroupInt(filepath.Join(base, "memory.current")), readCgroupInt(filepath.Join(base, "memory.max"))
}

// readCgroupInt reads a single-integer cgroup file; a missing file, "max"
// (unlimited), or unparseable content all yield -1.
func readCgroupInt(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// currentDropIn reads the on-disk hardening drop-in for a unit.
func (u *ubuntu) currentDropIn(svc string) (string, bool) {
	b, err := os.ReadFile(filepath.Join("/etc/systemd/system", svc+".d", "10-hardening.conf"))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// HostDisk reports filesystem usage for each path via df (best-effort; a path
// that can't be stat'd is skipped).
func (u *ubuntu) HostDisk(ctx context.Context, paths []string) []DiskStat {
	var out []DiskStat
	for _, p := range paths {
		o, err := exec.CommandContext(ctx, "df", "-B1", "--output=size,used,avail", p).Output()
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSpace(string(o)), "\n")
		if len(lines) < 2 {
			continue
		}
		f := strings.Fields(lines[len(lines)-1])
		if len(f) < 3 {
			continue
		}
		total, _ := strconv.ParseInt(f[0], 10, 64)
		used, _ := strconv.ParseInt(f[1], 10, 64)
		avail, _ := strconv.ParseInt(f[2], 10, 64)
		pct := 0
		if total > 0 {
			pct = int(used * 100 / total)
		}
		out = append(out, DiskStat{Path: p, TotalBytes: total, UsedBytes: used, AvailBytes: avail, UsePct: pct})
	}
	return out
}

// DirSize returns the total size of a directory in bytes via du, or -1 on error
// (e.g. the path doesn't exist).
func (u *ubuntu) DirSize(ctx context.Context, path string) int64 {
	o, err := exec.CommandContext(ctx, "du", "-sb", path).Output()
	if err != nil {
		return -1
	}
	f := strings.Fields(string(o))
	if len(f) == 0 {
		return -1
	}
	n, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// systemctlValue returns a single unit property value ("" on error/unset).
func systemctlValue(ctx context.Context, svc, prop string) string {
	out, err := exec.CommandContext(ctx, "systemctl", "show", "-p", prop, "--value", svc).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// parseBytes turns a systemd byte value into an int64; "infinity", "[not set]",
// and unparseable values become -1 (unknown/unlimited).
func parseBytes(v string) int64 {
	if v == "" || v == "infinity" || v == "[not set]" {
		return -1
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return -1
	}
	return n
}
