package runner

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/erlete/srm/internal/config"
)

// Windows Job Object resource caps for ephemeral jobs.
//
// The Linux build bounds a runner's resources with a systemd cgroup (MemoryMax /
// TasksMax in the unit drop-in, plus a host-wide srm.slice). Windows has no cgroup;
// its analog is the Job Object - a kernel container a process tree is assigned to,
// carrying memory and process-count limits. A Job Object's limits only live as long
// as some handle to it stays open, so it has no use for the PERSISTENT runner (a
// config.cmd-installed service whose process srm does not own and cannot keep a
// handle for from a transient CLI) - which is why Inspect reports its memory caps as
// unknown. But the EPHEMERAL lane fits perfectly: the long-lived supervisor service
// runs each job via exec, so it can create a Job Object, assign the job to it, and
// hold the handle for exactly the job's duration. See jobobject_windows.go.
//
// This file holds the OS-agnostic limit math (so it is unit-testable on any platform);
// the syscalls live in jobobject_windows.go with a jobobject_other.go stub.

// jobLimits are the caps applied to one ephemeral job's Job Object, derived from
// config.ResourceLimits. A zero field means "no cap for that dimension".
type jobLimits struct {
	memBytes        uint64 // total committed-memory cap for the whole job tree (cgroup memory.max analog)
	activeProcesses uint32 // max concurrent processes in the job (cgroup pids.max analog)
}

func (l jobLimits) isZero() bool { return l.memBytes == 0 && l.activeProcesses == 0 }

// deriveJobLimits maps the cgroup-shaped ResourceLimits onto the Job Object caps that
// have a faithful Windows analog: MemoryMax -> total job memory limit, TasksMax ->
// active process limit. The others have NO Job Object equivalent and are deliberately
// ignored: MemoryHigh is a soft reclaim threshold (Windows jobs have no soft cap),
// MemorySwapMax governs swap (no per-job analog), and CPUWeight is a RELATIVE share
// under contention, not the HARD percentage a Job Object's CPU rate control enforces -
// mapping it would silently change its meaning. totalRAM resolves systemd percentage
// values (e.g. "25%"); pass 0 when it is unknown, which treats percentages as unset.
func deriveJobLimits(r config.ResourceLimits, totalRAM uint64) jobLimits {
	var l jobLimits
	if b, ok := parseSystemdBytes(r.MemoryMax, totalRAM); ok {
		l.memBytes = b
	}
	if n, err := strconv.ParseUint(strings.TrimSpace(r.TasksMax), 10, 32); err == nil && n > 0 {
		l.activeProcesses = uint32(n)
	}
	return l
}

// parseSystemdBytes parses a systemd-style memory value into bytes. It accepts a bare
// integer (bytes), a K/M/G/T suffix (1024-based, as systemd uses), or a percentage of
// totalRAM (e.g. "25%"). "", "0", and "infinity" mean no limit and yield (0, false);
// a percentage when totalRAM is 0 (unknown) likewise yields no limit. Mirrors the
// values config.ResourceLimits documents passing through to systemd.
func parseSystemdBytes(s string, totalRAM uint64) (uint64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" || s == "infinity" {
		return 0, false
	}
	if strings.HasSuffix(s, "%") {
		pct, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "%")), 64)
		if err != nil || pct <= 0 || totalRAM == 0 {
			return 0, false
		}
		return uint64(float64(totalRAM) * pct / 100.0), true
	}
	mult := uint64(1)
	num := s
	switch s[len(s)-1] {
	case 'K', 'k':
		mult, num = 1<<10, s[:len(s)-1]
	case 'M', 'm':
		mult, num = 1<<20, s[:len(s)-1]
	case 'G', 'g':
		mult, num = 1<<30, s[:len(s)-1]
	case 'T', 't':
		mult, num = 1<<40, s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return uint64(n * float64(mult)), true
}

// formatJobLimits renders jobLimits for the supervisor's startup log so an operator can
// see, in the lane's own log, exactly what runConfinedJob will enforce - including the
// silent case where nothing is capped, which otherwise looks identical to a healthy run
// (the footgun: a config cap that never reached the running supervisor, e.g. a hand-edit
// that didn't parse or a lane not recreated after the change). It reuses the per-job
// "job confined" line's memBytes/activeProcesses vocabulary so the two correlate.
func formatJobLimits(l jobLimits) string {
	if l.isZero() {
		return "UNCAPPED (no resource limits applied)"
	}
	var parts []string
	if l.memBytes > 0 {
		parts = append(parts, fmt.Sprintf("memBytes=%d", l.memBytes))
	}
	if l.activeProcesses > 0 {
		parts = append(parts, fmt.Sprintf("activeProcesses=%d", l.activeProcesses))
	}
	return strings.Join(parts, " ")
}
