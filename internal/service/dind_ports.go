//go:build !windows

package service

import (
	"context"
	"sort"

	"github.com/erlete/srm/internal/runner"
)

// DinDPortRanges reports THIS host's rootless-DinD published-port partitioning for
// orgs: each org's dind.portRange pool, and for every installed ephemeral slot the
// block it should get vs the range its running rootless dockerd actually uses (read
// from the slot's network namespace; needs root). Observational: it never mutates.
// Enabled=false (nothing gathered) when docker.rootlessDinD is off.
func (m *Manager) DinDPortRanges(ctx context.Context, orgs []string) DinDPortReport {
	if !m.cfg.Docker.RootlessDinD {
		return DinDPortReport{}
	}
	var slots []localSlot
	if units, err := m.orchestratorFor("").ListEphemeralUnits(ctx); err == nil {
		orgsByLen := append([]string{}, m.cfg.OrgNames()...)
		sort.Slice(orgsByLen, func(i, j int) bool { return len(orgsByLen[i]) > len(orgsByLen[j]) })
		for _, svc := range units {
			if org, slot, ok := parseEphemeralUnitName(svc, orgsByLen); ok {
				slots = append(slots, localSlot{org, slot})
			}
		}
	}
	read := func(org, slot string) (int, int, error) { return runner.ReadDinDPortRange(ctx, org, slot) }
	return buildDinDPortReport(m.cfg, orgs, slots, read, readSysctl("/proc/sys/net/ipv4/ip_local_port_range"))
}
