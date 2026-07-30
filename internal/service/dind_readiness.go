//go:build !windows

package service

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/erlete/srm/internal/runner"
)

// DinDReadiness probes THIS host's prerequisites for the per-job rootless Docker
// lane, but only when docker.rootlessDinD is enabled: the binaries rootlesskit
// shells out to, the newuidmap setuid bit (the classic "starts but cannot map uids"
// trap), /dev/fuse, unprivileged user namespaces, and a subuid/subgid range for each
// org's runner user (srm allocates these in EnsureBase, so a missing one means the
// slot was never created on this host). Observational: it never mutates or errors.
// Returns Enabled=false (no checks) when rootless DinD is off. orgs scopes the
// per-user subuid/subgid probe; single-user mode reports the shared user once.
func (m *Manager) DinDReadiness(orgs []string) DinDReport {
	if !m.cfg.Docker.RootlessDinD {
		return DinDReport{}
	}
	rep := DinDReport{
		Enabled:      true,
		CrossOrgRisk: !m.cfg.Isolation.PerOrgUsers && len(orgs) > 1,
	}
	// Every probe here is a HARD prerequisite: rootless dockerd / buildx needs each one,
	// and a missing one fails a build mid-job (silently queuing the DinD fleet) rather
	// than erroring at startup - so each failure is a BLOCKER, not an advisory.
	add := func(name string, ok bool, detail string) {
		rep.Checks = append(rep.Checks, DinDCheck{Name: name, OK: ok, Hard: true, Detail: detail})
	}

	// Binaries rootless dockerd / buildx need on PATH.
	for _, bin := range []string{"dockerd-rootless.sh", "rootlesskit", "newuidmap", "newgidmap", "slirp4netns", "fuse-overlayfs", "docker"} {
		if p, err := exec.LookPath(bin); err == nil {
			add(bin, true, p)
		} else {
			add(bin, false, "(not found - install rootless deps via `srm provision`)")
		}
	}

	// newuidmap MUST be setuid-root or rootlesskit cannot write the uid map.
	if p, err := exec.LookPath("newuidmap"); err == nil {
		if fi, err := os.Stat(p); err == nil && fi.Mode()&os.ModeSetuid != 0 {
			add("newuidmap setuid", true, "setuid bit present")
		} else {
			add("newuidmap setuid", false, "MISSING setuid bit (rootless uid mapping will fail) - reinstall `uidmap`")
		}
	}

	// /dev/fuse for fuse-overlayfs.
	if _, err := os.Stat("/dev/fuse"); err == nil {
		add("/dev/fuse", true, "present")
	} else {
		add("/dev/fuse", false, "absent (fuse-overlayfs storage driver unavailable)")
	}

	// Unprivileged user namespaces must be enabled (rootless dockerd clones one).
	usernsOK, usernsDetail := usernsReadiness(
		readSysctl("/proc/sys/kernel/unprivileged_userns_clone"),
		readSysctl("/proc/sys/user/max_user_namespaces"))
	add("userns", usernsOK, usernsDetail)

	// A subuid/subgid range per org's runner user (EnsureBase allocates these). Exact
	// first-field match (not substring), and the COUNT is checked: an undersized range
	// (hand-edited / distro-seeded) can't map the full userns and rootless dockerd
	// fails mid-job, so flag it rather than green it - matching ensureSubIDFile.
	subuid := readSysctl("/etc/subuid")
	subgid := readSysctl("/etc/subgid")
	seen := map[string]bool{}
	for _, org := range orgs {
		user := m.cfg.RunnerUserFor(org)
		if seen[user] { // single-user mode: all orgs share one user/range - report it once
			continue
		}
		seen[user] = true
		uidN, gidN := subIDRangeCount(subuid, user), subIDRangeCount(subgid, user)
		switch {
		case uidN == 0 || gidN == 0:
			add("subuid/subgid", false, user+": MISSING (create a slot to allocate, or check /etc/subuid)")
		case uidN < runner.SubIDCount || gidN < runner.SubIDCount:
			add("subuid/subgid", false, fmt.Sprintf("%s: TOO SMALL (uid=%d gid=%d, need >= %d) - widen or remove the line", user, uidN, gidN, runner.SubIDCount))
		default:
			add("subuid/subgid", true, fmt.Sprintf("%s: allocated (%d)", user, uidN))
		}
	}
	return rep
}
