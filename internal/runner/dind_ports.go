package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// dindPortWrapperName is the per-cycle dockerd wrapper srm writes into the slot's
// runtime dir when the org partitions published ports (dind.portRange).
const dindPortWrapperName = "srm-dockerd"

// dindStateSubdir is dockerd-rootless.sh's default RootlessKit state dir under
// XDG_RUNTIME_DIR (srm does not override DOCKERD_ROOTLESS_ROOTLESSKIT_STATE_DIR).
// Under --detach-netns the slot's network namespace is bind-mounted at <state>/netns.
const dindStateSubdir = "dockerd-rootless"

// ErrDinDNotRunning reports that a slot has no live rootless daemon network namespace
// to read (the lane is between cycles, stopped, or not a DinD lane).
var ErrDinDNotRunning = errors.New("no running rootless dockerd for this slot")

// renderDinDPortWrapper returns the dockerd wrapper that pins a slot's published-port
// range. dockerd-rootless.sh execs "${DOCKERD:-dockerd}" at the end of its child stage
// (inside RootlessKit's user + mount namespaces, after it has itself written
// net.ipv4.ip_forward into the same network namespace the same way), so pointing
// DOCKERD here runs this script first.
//
// dockerd's port allocator reads net.ipv4.ip_local_port_range once, on its first use,
// from the network namespace where it maps ports: RootlessKit's detached netns
// ($ROOTLESSKIT_STATE_DIR/netns) under --detach-netns, else the daemon's own. Pinning
// the slot's block there before dockerd starts keeps every dynamic host port this
// daemon hands out inside the block, so sibling slots can no longer pick the same port.
// The host's own sysctl is never touched.
//
// Fail-open: if the write fails, dockerd still starts on the kernel default range
// (the pre-partitioning behavior) and the reason lands in the slot's journal.
func renderDinDPortWrapper(lo, hi int) string {
	rng := strconv.Itoa(lo) + " " + strconv.Itoa(hi)
	return `#!/bin/sh
# Managed by srm (rewritten every ephemeral cycle). Pins this slot's published-port
# range in the network namespace where the rootless dockerd maps ports, then execs
# dockerd. See dind.portRange in docs/CONFIGURATION.md.
netns=/proc/self/ns/net
if [ -n "$ROOTLESSKIT_STATE_DIR" ] && [ -e "$ROOTLESSKIT_STATE_DIR/netns" ]; then
	netns="$ROOTLESSKIT_STATE_DIR/netns"
fi
if ! nsenter -n"$netns" sysctl -q -w net.ipv4.ip_local_port_range="` + rng + `"; then
	echo "srm: could not pin the published-port range ` + strconv.Itoa(lo) + "-" + strconv.Itoa(hi) + `; dockerd keeps the default range" >&2
fi
exec dockerd "$@"
`
}

// writeDinDPortWrapper writes the slot's dockerd wrapper into rt and returns the
// DOCKERD=... entry for the daemon's environment. The wrapper is root-owned and
// world-readable/executable (no secrets in it) so the per-org user can exec it.
func writeDinDPortWrapper(rt string, lo, hi int) (string, error) {
	p := filepath.Join(rt, dindPortWrapperName)
	if err := os.WriteFile(p, []byte(renderDinDPortWrapper(lo, hi)), 0o755); err != nil {
		return "", err
	}
	// WriteFile's mode is umask-masked; force it so the per-org user can exec it.
	if err := os.Chmod(p, 0o755); err != nil {
		return "", err
	}
	return "DOCKERD=" + p, nil
}

// dinDPortEnv resolves the slot's published-port block from the org's dind settings
// and, when partitioning is on, writes the wrapper and returns its DOCKERD entry plus
// a one-line note for the slot's journal. Partitioning off returns ("", "", nil): the
// daemon env is unchanged. A bad setting (malformed pool, slot past the pool's end)
// returns an error so the caller logs it and starts the daemon on the default range
// rather than failing the job.
func (u *ubuntu) dinDPortEnv(rt, org, slot string) (envEntry, note string, err error) {
	if !u.opts.DinDPorts.PortsEnabled() {
		return "", "", nil
	}
	lo, hi, err := u.opts.DinDPorts.SlotPortRange(slot)
	if err != nil {
		return "", "", err
	}
	envEntry, err = writeDinDPortWrapper(rt, lo, hi)
	if err != nil {
		return "", "", fmt.Errorf("write dockerd port wrapper: %w", err)
	}
	return envEntry, fmt.Sprintf("srm: %s/%s rootless dockerd published-port range %d-%d", org, slot, lo, hi), nil
}

// dinDStateDir is a slot's RootlessKit state dir (child_pid, netns, api.sock, ...).
func dinDStateDir(org, slot string) string {
	return filepath.Join(dindRunRoot, org, slot, dindStateSubdir)
}

// dinDNetnsViaChild returns a path that opens the slot's detached network namespace
// from the HOST mount namespace. RootlessKit bind-mounts the netns onto
// <state>/netns only inside its own mount namespace (on the host that file is an
// empty placeholder), so the path goes through the RootlessKit child's root,
// /proc/<child_pid>/root<state>/netns - the same path RootlessKit hands slirp4netns.
// A missing or malformed child_pid means the daemon is not running.
func dinDNetnsViaChild(state string) (string, error) {
	b, err := os.ReadFile(filepath.Join(state, "child_pid"))
	if err != nil {
		return "", ErrDinDNotRunning
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid < 1 {
		return "", ErrDinDNotRunning
	}
	return "/proc/" + strconv.Itoa(pid) + "/root" + filepath.ToSlash(state) + "/netns", nil
}

// parsePortRangeSysctl parses the "LO\tHI" contents of ip_local_port_range.
func parsePortRangeSysctl(s string) (lo, hi int, err error) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("unexpected ip_local_port_range %q", strings.TrimSpace(s))
	}
	if lo, err = strconv.Atoi(f[0]); err != nil {
		return 0, 0, fmt.Errorf("unexpected ip_local_port_range %q", strings.TrimSpace(s))
	}
	if hi, err = strconv.Atoi(f[1]); err != nil {
		return 0, 0, fmt.Errorf("unexpected ip_local_port_range %q", strings.TrimSpace(s))
	}
	return lo, hi, nil
}
