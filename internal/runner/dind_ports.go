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
// The same sysctl is also the source-port range for the connections made INSIDE that
// netns, and every inbound connection to a published port makes two of them: the
// RootlessKit child dials 127.0.0.1:<port>, and docker-proxy dials <container>:<port>
// over the bridge. When the client closes first (the normal HTTP/1.1 case), the
// docker-proxy dial parks its source port in TIME_WAIT for 60 s, and only loopback
// TIME_WAITs are reusable by default (tcp_tw_reuse=2), so a narrow block would cap a
// published port at about blockSize/60 new connections per second before connect()
// fails with EADDRNOTAVAIL - silently, as docker-proxy's output goes nowhere.
// tcp_tw_reuse=1 (per netns, outgoing connects only, safe with the default TCP
// timestamps) lets those TIME_WAITs be reused after a second, lifting the cap to
// roughly blockSize new connections per second. Measured live on Docker 29.8.2.
//
// Fail-open: if a write fails, dockerd still starts (on the kernel default range when
// the range write is the one that failed) and the reason lands in the slot's journal.
func renderDinDPortWrapper(lo, hi int) string {
	rng := strconv.Itoa(lo) + " " + strconv.Itoa(hi)
	return `#!/bin/sh
# Managed by srm (rewritten every ephemeral cycle). Pins this slot's published-port
# range in the network namespace where the rootless dockerd maps ports, lets the
# proxy hops in that namespace reuse TIME_WAIT source ports, then execs dockerd.
# See dind.portRange in docs/CONFIGURATION.md.
netns=/proc/self/ns/net
if [ -n "$ROOTLESSKIT_STATE_DIR" ] && [ -e "$ROOTLESSKIT_STATE_DIR/netns" ]; then
	netns="$ROOTLESSKIT_STATE_DIR/netns"
fi
if ! nsenter -n"$netns" sysctl -q -w net.ipv4.ip_local_port_range="` + rng + `"; then
	echo "srm: could not pin the published-port range ` + strconv.Itoa(lo) + "-" + strconv.Itoa(hi) + `; dockerd keeps the default range" >&2
fi
if ! nsenter -n"$netns" sysctl -q -w net.ipv4.tcp_tw_reuse=1; then
	echo "srm: could not enable tcp_tw_reuse; short connections to published ports may run out of source ports" >&2
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

// DinDNetns is what a slot's running rootless dockerd network namespace actually uses.
type DinDNetns struct {
	PortLo, PortHi int // net.ipv4.ip_local_port_range
	TWReuse        int // net.ipv4.tcp_tw_reuse (srm sets 1 when the org partitions ports)
}

// dinDNetnsReadArgs is the command run inside the slot's namespace: both sysctls in
// one exec, parsed by parseDinDNetns.
var dinDNetnsReadArgs = []string{"cat", "/proc/sys/net/ipv4/ip_local_port_range", "/proc/sys/net/ipv4/tcp_tw_reuse"}

// parseDinDNetns parses "LO\tHI\nREUSE\n" (ip_local_port_range then tcp_tw_reuse).
func parseDinDNetns(s string) (DinDNetns, error) {
	f := strings.Fields(s)
	bad := fmt.Errorf("unexpected slot network namespace sysctls %q", strings.TrimSpace(s))
	if len(f) != 3 {
		return DinDNetns{}, bad
	}
	var n [3]int
	for i, v := range f {
		x, err := strconv.Atoi(v)
		if err != nil {
			return DinDNetns{}, bad
		}
		n[i] = x
	}
	return DinDNetns{PortLo: n[0], PortHi: n[1], TWReuse: n[2]}, nil
}
