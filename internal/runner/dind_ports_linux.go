//go:build linux

package runner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ReadDinDPortRange reads the published-port range a slot's running rootless dockerd
// actually uses: net.ipv4.ip_local_port_range inside the slot's detached network
// namespace, entered through the RootlessKit child (see dinDNetnsViaChild). Needs
// root, but not the slot's user namespace. ErrDinDNotRunning means the slot has no
// live daemon right now (between cycles, stopped, or not a DinD lane).
//
// It execs nsenter rather than calling setns in-process, so no Go thread is ever left
// in a foreign namespace.
func ReadDinDPortRange(ctx context.Context, org, slot string) (lo, hi int, err error) {
	ns, err := dinDNetnsViaChild(dinDStateDir(org, slot))
	if err != nil {
		return 0, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nsenter", "--net="+ns, "cat", "/proc/sys/net/ipv4/ip_local_port_range").CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		// A child_pid left by a stopped daemon points at a gone (or reused) process: the
		// path is missing, or is not a namespace and nsenter refuses it. Not running.
		if strings.Contains(msg, "Invalid argument") || strings.Contains(msg, "No such file") {
			return 0, 0, ErrDinDNotRunning
		}
		if strings.Contains(msg, "Operation not permitted") || strings.Contains(msg, "Permission denied") {
			return 0, 0, errors.New("needs root to enter the slot's network namespace")
		}
		return 0, 0, fmt.Errorf("read slot network namespace range: %v: %s", err, msg)
	}
	return parsePortRangeSysctl(string(out))
}
