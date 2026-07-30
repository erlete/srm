package service

import (
	"os"
	"strconv"
	"strings"
)

// DinDCheck is one rootless-Docker host-readiness probe result: a short label, an
// OK flag (satisfied), and a detail (the resolved path / value when OK, or a
// remediation hint when not). Hard marks a prerequisite whose absence breaks rootless
// DinD mid-job (silently queuing the fleet) rather than a soft advisory - a failed
// Hard check is a BLOCKER that doctor/Health surface loudly and `srm provision
// --rootless-dind` remediates.
type DinDCheck struct {
	Name   string
	OK     bool
	Hard   bool
	Detail string
}

// DinDReport is this host's rootless-Docker readiness, gathered only when
// docker.rootlessDinD is enabled. Enabled=false (no checks) means the probe was
// skipped: DinD is off, or this is a host with no rootless-Docker analog (Windows).
// CrossOrgRisk is set when isolation.perOrgUsers is off with more than one org - the
// per-JOB clean-slate still holds, but there is no cross-ORG uid/data boundary.
type DinDReport struct {
	Enabled      bool
	CrossOrgRisk bool
	Checks       []DinDCheck
}

// AllOK reports whether every gathered check passed (vacuously true when there are
// no checks, e.g. DinD disabled).
func (r DinDReport) AllOK() bool {
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// Blockers returns the failed HARD checks - the prerequisites whose absence breaks
// rootless DinD (and thus silently queues every DinD job) until fixed.
func (r DinDReport) Blockers() []DinDCheck {
	var out []DinDCheck
	for _, c := range r.Checks {
		if c.Hard && !c.OK {
			out = append(out, c)
		}
	}
	return out
}

// HasBlocker reports whether any hard prerequisite is unmet.
func (r DinDReport) HasBlocker() bool { return len(r.Blockers()) > 0 }

// usernsReadiness reports whether unprivileged user namespaces (which rootless
// dockerd clones) are enabled, from the two kernel knobs, plus a human detail.
// unprivileged_userns_clone, WHEN PRESENT, is authoritative: =0 actively blocks
// unprivileged userns even if user.max_user_namespaces is a large positive default
// (some hardened Debian-derived kernels ship exactly that), so it is checked FIRST -
// reporting "enabled" off max_user_namespaces there would false-green a host where
// DinD fails mid-job.
func usernsReadiness(clone, maxns string) (bool, string) {
	clone, maxns = strings.TrimSpace(clone), strings.TrimSpace(maxns)
	switch {
	case clone == "0":
		return false, "DISABLED (unprivileged_userns_clone=0) - set kernel.unprivileged_userns_clone=1"
	case clone == "1":
		return true, "enabled (unprivileged_userns_clone=1)"
	case maxns != "" && maxns != "0":
		return true, "enabled (max_user_namespaces=" + maxns + ")"
	default:
		return false, "likely DISABLED - set kernel.unprivileged_userns_clone=1 / user.max_user_namespaces>0"
	}
}

// subIDRangeCount returns the COUNT field of user's range in file (the contents of
// /etc/subuid or /etc/subgid), or 0 if the user has no range or it can't be parsed.
// Mirrors ensureSubIDFile's exact-first-field parse so probe and allocator agree (a
// substring test would match "acme" off "srm-acme").
func subIDRangeCount(file, user string) int {
	for line := range strings.SplitSeq(file, "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) == 3 && f[0] == user {
			n, _ := strconv.Atoi(f[2])
			return n
		}
	}
	return 0
}

// readSysctl returns the trimmed contents of a /proc or /etc file, or "" if it can't
// be read (the caller treats absent as "unknown/disabled").
func readSysctl(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
