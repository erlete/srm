package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// DefaultDinDPortsPerSlot is the size of each ephemeral slot's published-port block
// when dind.portRange is set and dind.portsPerSlot is left at 0. A job's dynamic
// published ports (`docker run -p 0:80`, `-P`, testcontainers) come from its slot's
// block, so this bounds how many such ports ONE job can hold at once.
const DefaultDinDPortsPerSlot = 1000

// minDinDPort is the lowest port a slot range may start at. The kernel rejects an
// ip_local_port_range below net.ipv4.ip_unprivileged_port_start (1024 by default),
// and a rootless daemon cannot publish a privileged port anyway.
const minDinDPort = 1024

// maxDinDPort is the highest valid TCP/UDP port.
const maxDinDPort = 65535

// OrgDinD holds per-org settings for the rootless-DinD ephemeral lane (only consulted
// when docker.rootlessDinD is on; Linux only).
//
// PortRange partitions the host's dynamically published ports between this org's
// slots. Every slot's rootless dockerd runs its port mapping in its own network
// namespace, whose kernel default ip_local_port_range (32768-60999) is identical for
// all of them, so each daemon hands out 32768, 32769, ... and RootlessKit then fails
// to bind the same port on the host ("address already in use"). With PortRange set,
// slot N gets its own block of PortsPerSlot ports inside it:
//
//	[start + (N-1)*PortsPerSlot, start + N*PortsPerSlot - 1]
//
// pinned into the slot's network namespace before dockerd starts. Explicit host
// ports (`-p 8080:80`) are not affected. Pools of different orgs on one host must not
// overlap. Empty PortRange (the default) leaves the daemon on the kernel default,
// byte-identical to before.
type OrgDinD struct {
	// PortRange is this org's published-port pool, "START-END" (inclusive), e.g.
	// "10000-29999". Empty disables per-slot partitioning for the org.
	PortRange string `koanf:"portRange" yaml:"portRange,omitempty"`
	// PortsPerSlot is the block size per slot. 0 means DefaultDinDPortsPerSlot.
	PortsPerSlot int `koanf:"portsPerSlot" yaml:"portsPerSlot,omitempty"`
}

// IsZero reports whether no per-org DinD setting is configured (yaml omitempty).
func (d OrgDinD) IsZero() bool { return d == OrgDinD{} }

// PortsEnabled reports whether per-slot published-port partitioning is configured.
func (d OrgDinD) PortsEnabled() bool { return strings.TrimSpace(d.PortRange) != "" }

// ParsePortRange parses an inclusive "START-END" port range and checks it is a valid,
// unprivileged range (1024 <= START <= END <= 65535).
func ParsePortRange(s string) (lo, hi int, err error) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return 0, 0, fmt.Errorf("port range %q: want START-END, e.g. 10000-29999", s)
	}
	lo, err = strconv.Atoi(strings.TrimSpace(a))
	if err != nil {
		return 0, 0, fmt.Errorf("port range %q: start is not a number", s)
	}
	hi, err = strconv.Atoi(strings.TrimSpace(b))
	if err != nil {
		return 0, 0, fmt.Errorf("port range %q: end is not a number", s)
	}
	if lo < minDinDPort || hi > maxDinDPort || lo > hi {
		return 0, 0, fmt.Errorf("port range %q: must satisfy %d <= START <= END <= %d", s, minDinDPort, maxDinDPort)
	}
	return lo, hi, nil
}

// Pool returns the validated pool bounds and the effective block size. It errors when
// partitioning is not configured, the range is malformed, or the block size is
// negative or larger than the whole pool.
func (d OrgDinD) Pool() (lo, hi, perSlot int, err error) {
	if !d.PortsEnabled() {
		return 0, 0, 0, fmt.Errorf("dind.portRange is not set")
	}
	lo, hi, err = ParsePortRange(d.PortRange)
	if err != nil {
		return 0, 0, 0, err
	}
	perSlot = d.PortsPerSlot
	if perSlot == 0 {
		perSlot = DefaultDinDPortsPerSlot
	}
	if perSlot < 1 {
		return 0, 0, 0, fmt.Errorf("dind.portsPerSlot %d: must be positive", d.PortsPerSlot)
	}
	if perSlot > hi-lo+1 {
		return 0, 0, 0, fmt.Errorf("dind.portsPerSlot %d is larger than the pool %s (%d ports)", perSlot, d.PortRange, hi-lo+1)
	}
	return lo, hi, perSlot, nil
}

// SlotCapacity returns how many slots fit in the pool (slots 1..N get a block).
func (d OrgDinD) SlotCapacity() (int, error) {
	lo, hi, perSlot, err := d.Pool()
	if err != nil {
		return 0, err
	}
	return (hi - lo + 1) / perSlot, nil
}

// SlotPortRange returns the published-port block for an ephemeral slot id (a
// positive integer). It errors when partitioning is off, the pool is invalid, the
// slot id is not numeric, or the slot's block does not fit inside the pool.
func (d OrgDinD) SlotPortRange(slot string) (lo, hi int, err error) {
	poolLo, poolHi, perSlot, err := d.Pool()
	if err != nil {
		return 0, 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(slot))
	if err != nil || n < 1 {
		return 0, 0, fmt.Errorf("slot %q is not a positive slot number", slot)
	}
	lo = poolLo + (n-1)*perSlot
	hi = lo + perSlot - 1
	if hi > poolHi {
		capacity := (poolHi - poolLo + 1) / perSlot
		return 0, 0, fmt.Errorf("slot %d does not fit in dind.portRange %s with %d ports per slot (room for slots 1-%d)", n, d.PortRange, perSlot, capacity)
	}
	return lo, hi, nil
}

// DinDPortIssues validates every org's dind port settings and reports, in a stable
// order, each malformed pool and each pair of orgs whose pools overlap (overlapping
// pools let two orgs' slots publish the same host port). It never errors: a bad
// setting makes the affected slots fall back to the kernel default range at runtime,
// so callers surface these as warnings (doctor, config writes).
func (c *Config) DinDPortIssues() []string {
	type pool struct {
		org    string
		lo, hi int
	}
	var issues []string
	var pools []pool
	for _, oc := range c.Orgs {
		if !oc.DinD.PortsEnabled() {
			continue
		}
		lo, hi, _, err := oc.DinD.Pool()
		if err != nil {
			issues = append(issues, fmt.Sprintf("org %s: %v", oc.Name, err))
			continue
		}
		pools = append(pools, pool{oc.Name, lo, hi})
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i].lo < pools[j].lo })
	for i := 1; i < len(pools); i++ {
		for j := 0; j < i; j++ {
			if pools[j].hi >= pools[i].lo {
				issues = append(issues, fmt.Sprintf("orgs %s and %s: dind.portRange pools overlap (%d-%d and %d-%d)",
					pools[j].org, pools[i].org, pools[j].lo, pools[j].hi, pools[i].lo, pools[i].hi))
			}
		}
	}
	return issues
}
