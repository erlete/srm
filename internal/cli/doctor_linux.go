//go:build !windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/runner"
)

// dindReadiness prints the rootless-Docker host readiness probe when docker.rootlessDinD
// is on (Linux only - DinD has no Windows analog; doctor_windows.go stubs this).
func dindReadiness(cfg *config.Config, orgs []string) {
	if cfg.Docker.RootlessDinD {
		printDinDReadiness(cfg, orgs)
	}
}

// printDinDReadiness probes the host prerequisites for the per-job rootless Docker
// lane: the binaries rootlesskit shells out to, the newuidmap setuid bit (the classic
// "starts but cannot map uids" trap), /dev/fuse, unprivileged user namespaces, and a
// subuid/subgid range for each org's runner user (srm allocates these in EnsureBase,
// so a missing one means the slot was never created on this host). Observational - it
// prints findings, never errors.
func printDinDReadiness(cfg *config.Config, orgs []string) {
	fmt.Println()
	fmt.Println("rootless docker (docker.rootlessDinD on):")

	// Per-JOB clean-slate always holds (each job wipes its data-root). The cross-ORG
	// uid/data boundary, however, exists only under isolation.perOrgUsers - without it
	// every org shares one runner user, one subuid block, and one data-root owner.
	if !cfg.Isolation.PerOrgUsers && len(orgs) > 1 {
		fmt.Println("  WARNING: isolation.perOrgUsers is off with >1 org - rootless DinD gives")
		fmt.Println("           per-JOB clean-slate but NO cross-ORG boundary (shared user/subuid/")
		fmt.Println("           data-root). Enable isolation.perOrgUsers for a cross-org boundary.")
	}

	// Binaries rootless dockerd / buildx need on PATH.
	for _, bin := range []string{"dockerd-rootless.sh", "rootlesskit", "newuidmap", "newgidmap", "slirp4netns", "fuse-overlayfs", "docker"} {
		if p, err := exec.LookPath(bin); err == nil {
			fmt.Printf("  %-18s %s\n", bin, p)
		} else {
			fmt.Printf("  %-18s (not found - install rootless deps via `srm provision`)\n", bin)
		}
	}

	// newuidmap MUST be setuid-root or rootlesskit cannot write the uid map.
	if p, err := exec.LookPath("newuidmap"); err == nil {
		if fi, err := os.Stat(p); err == nil && fi.Mode()&os.ModeSetuid != 0 {
			fmt.Printf("  %-18s setuid bit present\n", "newuidmap setuid")
		} else {
			fmt.Printf("  %-18s MISSING setuid bit (rootless uid mapping will fail) - reinstall `uidmap`\n", "newuidmap setuid")
		}
	}

	// /dev/fuse for fuse-overlayfs.
	if _, err := os.Stat("/dev/fuse"); err == nil {
		fmt.Printf("  %-18s present\n", "/dev/fuse")
	} else {
		fmt.Printf("  %-18s absent (fuse-overlayfs storage driver unavailable)\n", "/dev/fuse")
	}

	// Unprivileged user namespaces must be enabled (rootless dockerd clones one).
	fmt.Printf("  %-18s %s\n", "userns", usernsReadiness(
		readSysctl("/proc/sys/kernel/unprivileged_userns_clone"),
		readSysctl("/proc/sys/user/max_user_namespaces")))

	// A subuid/subgid range per org's runner user (EnsureBase allocates these). Exact
	// first-field match (not substring) so "acme" is not false-greened by "srm-acme",
	// and the COUNT is checked: an undersized range (hand-edited / distro-seeded) can't
	// map the full userns and rootless dockerd fails mid-job, so flag it rather than
	// green it - matching what the ensureSubIDFile allocator now rejects.
	subuid := readSysctl("/etc/subuid")
	subgid := readSysctl("/etc/subgid")
	seen := map[string]bool{}
	for _, org := range orgs {
		user := cfg.RunnerUserFor(org)
		if seen[user] { // single-user mode: all orgs share one user/range - report it once
			continue
		}
		seen[user] = true
		uidN, gidN := subIDRangeCount(subuid, user), subIDRangeCount(subgid, user)
		switch {
		case uidN == 0 || gidN == 0:
			fmt.Printf("  %-18s %s: MISSING (create a slot to allocate, or check /etc/subuid)\n", "subuid/subgid", user)
		case uidN < runner.SubIDCount || gidN < runner.SubIDCount:
			fmt.Printf("  %-18s %s: TOO SMALL (uid=%d gid=%d, need >= %d) - widen or remove the line\n", "subuid/subgid", user, uidN, gidN, runner.SubIDCount)
		default:
			fmt.Printf("  %-18s %s: allocated (%d)\n", "subuid/subgid", user, uidN)
		}
	}
}

// usernsReadiness describes whether unprivileged user namespaces (which rootless dockerd
// clones) are enabled, from the two kernel knobs. unprivileged_userns_clone, WHEN
// PRESENT, is authoritative: =0 actively blocks unprivileged userns even if
// user.max_user_namespaces is a large positive default (some hardened Debian-derived
// kernels ship exactly that), so it is checked FIRST - reporting "enabled" off
// max_user_namespaces there would false-green a host where DinD fails mid-job.
func usernsReadiness(clone, maxns string) string {
	clone, maxns = strings.TrimSpace(clone), strings.TrimSpace(maxns)
	switch {
	case clone == "0":
		return "DISABLED (unprivileged_userns_clone=0) - set kernel.unprivileged_userns_clone=1"
	case clone == "1":
		return "enabled (unprivileged_userns_clone=1)"
	case maxns != "" && maxns != "0":
		return "enabled (max_user_namespaces=" + maxns + ")"
	default:
		return "likely DISABLED - set kernel.unprivileged_userns_clone=1 / user.max_user_namespaces>0"
	}
}

// subIDRangeCount returns the COUNT field of user's range in file (the contents of
// /etc/subuid or /etc/subgid), or 0 if the user has no range or it can't be parsed.
// Mirrors ensureSubIDFile's exact-first-field parse so probe and allocator agree (a
// substring test would match "acme" off "srm-acme").
func subIDRangeCount(file, user string) int {
	for _, line := range strings.Split(file, "\n") {
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
