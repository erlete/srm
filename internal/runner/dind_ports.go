package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// DinDPortWrapperPath is the static, root-owned dockerd wrapper srm installs for
// rootless-DinD slots whose org partitions published ports (dind.portRange). It lives
// on a root-owned, exec-capable filesystem: NOT under /run (mounted noexec on Ubuntu)
// and NOT in a directory the per-org user can write (root writes this file).
const DinDPortWrapperPath = "/usr/local/lib/srm/dockerd-port-wrapper"

// dinDPortRangeEnv carries a slot's block ("LO HI") from srm to the wrapper.
const dinDPortRangeEnv = "SRM_DIND_PORT_RANGE"

// dinDWrapperCheckArg makes the wrapper exit 0 at once: srm's pre-flight that the
// per-org user can really execute it where it is installed.
const dinDWrapperCheckArg = "--srm-check"

// dindStateSubdir is dockerd-rootless.sh's default RootlessKit state dir under
// XDG_RUNTIME_DIR (srm does not override DOCKERD_ROOTLESS_ROOTLESSKIT_STATE_DIR).
// Under --detach-netns the slot's network namespace is bind-mounted at <state>/netns.
const dindStateSubdir = "dockerd-rootless"

// ErrDinDNotRunning reports that a slot has no live rootless daemon network namespace
// to read (the lane is between cycles, stopped, or not a DinD lane).
var ErrDinDNotRunning = errors.New("no running rootless dockerd for this slot")

// renderDinDPortWrapper returns the dockerd wrapper. dockerd-rootless.sh execs
// "${DOCKERD:-dockerd}" at the end of its child stage (inside RootlessKit's user +
// mount namespaces, after it has itself written net.ipv4.ip_forward into the same
// network namespace the same way), so pointing DOCKERD here runs this script first.
// The script is identical for every slot; the slot's block arrives in
// SRM_DIND_PORT_RANGE.
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
func renderDinDPortWrapper() string {
	return `#!/bin/sh
# Managed by srm. dockerd-rootless.sh execs this in place of dockerd (DOCKERD=...) on
# rootless-DinD slots whose org sets dind.portRange. It pins the slot's published-port
# block (SRM_DIND_PORT_RANGE="LO HI") in the network namespace where the rootless
# dockerd maps ports, lets the proxy hops there reuse TIME_WAIT source ports, then
# execs dockerd. See dind.portRange in docs/CONFIGURATION.md.
if [ "$1" = "` + dinDWrapperCheckArg + `" ]; then
	exit 0
fi
netns=/proc/self/ns/net
if [ -n "$ROOTLESSKIT_STATE_DIR" ] && [ -e "$ROOTLESSKIT_STATE_DIR/netns" ]; then
	netns="$ROOTLESSKIT_STATE_DIR/netns"
fi
if [ -n "$` + dinDPortRangeEnv + `" ]; then
	if ! nsenter -n"$netns" sysctl -q -w net.ipv4.ip_local_port_range="$` + dinDPortRangeEnv + `"; then
		echo "srm: could not pin the published-port range $` + dinDPortRangeEnv + `; dockerd keeps the default range" >&2
	fi
	if ! nsenter -n"$netns" sysctl -q -w net.ipv4.tcp_tw_reuse=1; then
		echo "srm: could not enable tcp_tw_reuse; short connections to published ports may run out of source ports" >&2
	fi
fi
exec dockerd "$@"
`
}

// installDinDPortWrapper installs the wrapper at path (atomically, only when its
// content or mode differs) and refuses a directory that is not owned by srm's user
// or that others can write. Linux implementation in dind_ports_linux.go; a variable
// so tests can substitute it.
var installDinDPortWrapper = installDinDPortWrapperImpl

// probeDinDPortWrapper proves the per-org user can execute the installed wrapper
// (no noexec mount, traversable path, working interpreter) by running it in check
// mode. A failure means DOCKERD must not be passed. A variable so tests can
// substitute it.
var probeDinDPortWrapper = probeDinDPortWrapperImpl

// dinDPortEnv resolves the slot's published-port block from the org's dind settings
// and, when partitioning is on and the wrapper is installed and executable by the
// per-org user, returns the daemon env entries that apply it (DOCKERD + the block)
// plus a one-line note for the slot's journal. Partitioning off returns (nil, "",
// nil): the daemon env is unchanged. Any problem (malformed pool, slot past the
// pool's end, wrapper not installable or not executable) returns an error so the
// caller logs it and starts the daemon on the default range: a slot without its block
// is better than a slot without Docker.
func (u *ubuntu) dinDPortEnv(ctx context.Context, org, slot string, usr *user.User) (env []string, note string, err error) {
	if !u.opts.DinDPorts.PortsEnabled() {
		return nil, "", nil
	}
	lo, hi, err := u.opts.DinDPorts.SlotPortRange(slot)
	if err != nil {
		return nil, "", err
	}
	path := u.portWrapperPath()
	if err := installDinDPortWrapper(path); err != nil {
		return nil, "", fmt.Errorf("install %s: %w", path, err)
	}
	if err := probeDinDPortWrapper(ctx, usr, path); err != nil {
		return nil, "", fmt.Errorf("%s is not executable by %s: %w", path, usr.Username, err)
	}
	env = []string{"DOCKERD=" + path, dinDPortRangeEnv + "=" + strconv.Itoa(lo) + " " + strconv.Itoa(hi)}
	return env, fmt.Sprintf("srm: %s/%s rootless dockerd published-port range %d-%d", org, slot, lo, hi), nil
}

// portWrapperPath is where this orchestrator installs the dockerd wrapper
// (DinDPortWrapperPath unless a test overrides it).
func (u *ubuntu) portWrapperPath() string {
	if u.dinDWrapperPath != "" {
		return u.dinDWrapperPath
	}
	return DinDPortWrapperPath
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
