package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/runner"
)

// Kernel default net.ipv4.ip_local_port_range of a fresh network namespace: what a
// slot's rootless dockerd uses when its org does not partition published ports.
const (
	kernelDefaultPortLo = 32768
	kernelDefaultPortHi = 60999
)

// DinD published-port slot states (DinDPortSlot.State).
const (
	PortStatePinned     = "pinned"      // the daemon runs on exactly the configured block
	PortStatePending    = "pending"     // configured, but the running daemon predates it (next cycle applies it)
	PortStateDefault    = "default"     // org not partitioned: kernel default range
	PortStateNotApplied = "not applied" // configured but this slot cannot get a block (see WantErr)
	PortStateNotRunning = "not running" // no live daemon namespace to read right now
	PortStateUnreadable = "unreadable"  // the namespace exists but could not be read (see EffErr)
)

// DinDPortOrg is one org's published-port pool as configured.
type DinDPortOrg struct {
	Org         string
	Partitioned bool
	Pool        string // "START-END" when partitioned and valid
	PerSlot     int
	Capacity    int    // slots that fit in the pool
	Err         string // malformed pool / block size
}

// DinDPortSlot is one local ephemeral slot lane's published-port range: the block its
// org's dind.portRange assigns it (Want*) and the range its running rootless dockerd
// actually uses, read from the slot's network namespace (Eff*).
type DinDPortSlot struct {
	Org, Slot      string
	Partitioned    bool
	WantLo, WantHi int
	WantErr        string
	EffLo, EffHi   int
	EffErr         string
	State          string
}

// DinDPortReport is THIS host's rootless-DinD published-port partitioning: the pools,
// every local DinD slot's configured vs effective range, and warnings. Enabled=false
// means rootless DinD is off (or not applicable on this OS) and nothing was gathered.
type DinDPortReport struct {
	Enabled  bool
	Orgs     []DinDPortOrg
	Slots    []DinDPortSlot
	Warnings []string
}

// AnyPartitioned reports whether at least one org partitions published ports.
func (r DinDPortReport) AnyPartitioned() bool {
	for _, o := range r.Orgs {
		if o.Partitioned {
			return true
		}
	}
	return false
}

// localSlot is an installed ephemeral slot lane (org + slot id).
type localSlot struct{ org, slot string }

// portRangeReader reads a slot's effective range (runner.ReadDinDPortRange in prod).
type portRangeReader func(org, slot string) (lo, hi int, err error)

// buildDinDPortReport assembles the report from config, the host's installed slots,
// an effective-range reader and the host's own ip_local_port_range ("" if unknown).
// Pure (no host access) so it is unit-testable on any OS.
func buildDinDPortReport(cfg *config.Config, orgs []string, slots []localSlot, read portRangeReader, hostRange string) DinDPortReport {
	rep := DinDPortReport{Enabled: true}
	inScope := map[string]bool{}
	for _, o := range orgs {
		inScope[o] = true
	}

	type pool struct {
		org    string
		lo, hi int
	}
	var pools []pool
	for _, name := range orgs {
		oc, ok := cfg.Org(name)
		if !ok {
			continue
		}
		po := DinDPortOrg{Org: name, Partitioned: oc.DinD.PortsEnabled()}
		if po.Partitioned {
			if lo, hi, per, err := oc.DinD.Pool(); err != nil {
				po.Err = err.Error()
			} else {
				po.Pool, po.PerSlot, po.Capacity = fmt.Sprintf("%d-%d", lo, hi), per, (hi-lo+1)/per
				pools = append(pools, pool{name, lo, hi})
			}
		}
		rep.Orgs = append(rep.Orgs, po)
	}

	unpartitioned := map[string]int{}
	for _, ls := range slots {
		if !inScope[ls.org] {
			continue
		}
		oc, _ := cfg.Org(ls.org)
		s := DinDPortSlot{Org: ls.org, Slot: ls.slot}
		if oc != nil && oc.DinD.PortsEnabled() {
			s.Partitioned = true
			if lo, hi, err := oc.DinD.SlotPortRange(ls.slot); err != nil {
				s.WantErr = err.Error()
			} else {
				s.WantLo, s.WantHi = lo, hi
			}
		} else {
			unpartitioned[ls.org]++
		}
		lo, hi, err := read(ls.org, ls.slot)
		switch {
		case errors.Is(err, runner.ErrDinDNotRunning):
			s.EffErr = "not running"
		case err != nil:
			s.EffErr = err.Error()
		default:
			s.EffLo, s.EffHi = lo, hi
		}
		s.State = slotPortState(s)
		rep.Slots = append(rep.Slots, s)
	}
	sort.Slice(rep.Slots, func(i, j int) bool {
		if rep.Slots[i].Org != rep.Slots[j].Org {
			return rep.Slots[i].Org < rep.Slots[j].Org
		}
		return slotLess(rep.Slots[i].Slot, rep.Slots[j].Slot)
	})

	// Cross-org overlaps and malformed pools (the config-level checks).
	for _, issue := range cfg.DinDPortIssues() {
		for _, o := range orgs {
			if strings.Contains(issue, " "+o+" ") || strings.Contains(issue, " "+o+":") {
				rep.Warnings = append(rep.Warnings, issue)
				break
			}
		}
	}
	// Slots of unpartitioned orgs all draw from the kernel default range: they can
	// still collide with each other, and with any pool inside that range.
	if rep.AnyPartitioned() && len(unpartitioned) > 0 {
		total := 0
		names := make([]string, 0, len(unpartitioned))
		for o, n := range unpartitioned {
			names = append(names, o)
			total += n
		}
		sort.Strings(names)
		if total > 1 {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d DinD slot(s) of %s have no dind.portRange: they share the default range %d-%d and can still collide with each other",
				total, strings.Join(names, ", "), kernelDefaultPortLo, kernelDefaultPortHi))
		}
		for _, p := range pools {
			if p.lo <= kernelDefaultPortHi && p.hi >= kernelDefaultPortLo {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("org %s: pool %d-%d overlaps the default range %d-%d used by the unpartitioned slots",
					p.org, p.lo, p.hi, kernelDefaultPortLo, kernelDefaultPortHi))
			}
		}
	}
	// Advisory: the host's own outbound sockets take ephemeral ports from its
	// ip_local_port_range; a pool inside it can occasionally lose a port to them.
	if hlo, hhi, err := parseHostPortRange(hostRange); err == nil {
		for _, p := range pools {
			if p.lo <= hhi && p.hi >= hlo {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("org %s: pool %d-%d overlaps the host's ephemeral range %d-%d (net.ipv4.ip_local_port_range); a pool outside it avoids competing with the host's outbound sockets",
					p.org, p.lo, p.hi, hlo, hhi))
			}
		}
	}
	return rep
}

// slotPortState classifies a slot from its configured and effective ranges.
func slotPortState(s DinDPortSlot) string {
	switch {
	case s.Partitioned && s.WantErr != "":
		return PortStateNotApplied
	case s.EffErr == "not running":
		return PortStateNotRunning
	case s.EffErr != "":
		return PortStateUnreadable
	case !s.Partitioned:
		return PortStateDefault
	case s.EffLo == s.WantLo && s.EffHi == s.WantHi:
		return PortStatePinned
	default:
		return PortStatePending
	}
}

// parseHostPortRange parses the host's ip_local_port_range contents.
func parseHostPortRange(s string) (lo, hi int, err error) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("unknown")
	}
	if _, err := fmt.Sscan(f[0], &lo); err != nil {
		return 0, 0, err
	}
	if _, err := fmt.Sscan(f[1], &hi); err != nil {
		return 0, 0, err
	}
	return lo, hi, nil
}
