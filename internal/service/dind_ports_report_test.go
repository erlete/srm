package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/runner"
)

func portCfg(orgs ...config.OrgConfig) *config.Config { return &config.Config{Orgs: orgs} }

func netns(lo, hi, reuse int) runner.DinDNetns {
	return runner.DinDNetns{PortLo: lo, PortHi: hi, TWReuse: reuse}
}

func TestDinDPortReportStates(t *testing.T) {
	cfg := portCfg(
		config.OrgConfig{Name: "acme", DinD: config.OrgDinD{PortRange: "10000-11499", PortsPerSlot: 500}},
		config.OrgConfig{Name: "globex"},
	)
	slots := []localSlot{{"acme", "2"}, {"acme", "1"}, {"acme", "3"}, {"acme", "4"}, {"acme", "5"}, {"globex", "1"}}
	eff := map[string]runner.DinDNetns{
		"acme/1":   netns(10000, 10499, 1), // on its block with reuse: pinned
		"acme/2":   netns(32768, 60999, 2), // daemon predates the partition
		"acme/3":   netns(11000, 11499, 2), // block applied by v2.6.0, reuse not yet
		"acme/4":   netns(32768, 60999, 2), // slot 4 does not fit a 3-slot pool
		"globex/1": netns(32768, 60999, 2),
	}
	read := func(org, slot string) (runner.DinDNetns, error) {
		if r, ok := eff[org+"/"+slot]; ok {
			return r, nil
		}
		return runner.DinDNetns{}, runner.ErrDinDNotRunning
	}
	rep := buildDinDPortReport(cfg, []string{"acme", "globex"}, slots, read, "")
	if !rep.Enabled || !rep.AnyPartitioned() {
		t.Fatalf("report not enabled/partitioned: %+v", rep)
	}
	want := map[string]string{
		"acme/1":   PortStatePinned,
		"acme/2":   PortStatePending,
		"acme/3":   PortStatePending,
		"acme/4":   PortStateNotApplied,
		"acme/5":   PortStateNotApplied,
		"globex/1": PortStateDefault,
	}
	var order []string
	for _, s := range rep.Slots {
		order = append(order, s.Org+"/"+s.Slot)
		if s.State != want[s.Org+"/"+s.Slot] {
			t.Errorf("%s/%s state %q, want %q (%+v)", s.Org, s.Slot, s.State, want[s.Org+"/"+s.Slot], s)
		}
	}
	if strings.Join(order, ",") != "acme/1,acme/2,acme/3,acme/4,acme/5,globex/1" {
		t.Errorf("slots not ordered by org then slot number: %v", order)
	}
	if rep.Slots[0].EffTWReuse != 1 {
		t.Errorf("tcp_tw_reuse not carried into the slot: %+v", rep.Slots[0])
	}
	if rep.Orgs[0].Pool != "10000-11499" || rep.Orgs[0].Capacity != 3 || rep.Orgs[1].Partitioned {
		t.Errorf("org pools wrong: %+v", rep.Orgs)
	}
}

func TestDinDPortReportNotRunningAndUnreadable(t *testing.T) {
	cfg := portCfg(config.OrgConfig{Name: "acme", DinD: config.OrgDinD{PortRange: "10000-19999"}})
	read := func(_, slot string) (runner.DinDNetns, error) {
		if slot == "1" {
			return runner.DinDNetns{}, runner.ErrDinDNotRunning
		}
		return runner.DinDNetns{}, errors.New("needs root to enter the slot's network namespace")
	}
	rep := buildDinDPortReport(cfg, []string{"acme"}, []localSlot{{"acme", "1"}, {"acme", "2"}}, read, "")
	if rep.Slots[0].State != PortStateNotRunning || rep.Slots[1].State != PortStateUnreadable {
		t.Fatalf("states = %q, %q", rep.Slots[0].State, rep.Slots[1].State)
	}
}

func TestDinDPortReportWarnings(t *testing.T) {
	never := func(string, string) (runner.DinDNetns, error) { return runner.DinDNetns{}, runner.ErrDinDNotRunning }

	// Pools outside the default and host ranges, every DinD org partitioned: no warnings.
	clean := portCfg(
		config.OrgConfig{Name: "acme", DinD: config.OrgDinD{PortRange: "10000-29999", PortsPerSlot: 500}},
		config.OrgConfig{Name: "globex", DinD: config.OrgDinD{PortRange: "30000-31999", PortsPerSlot: 500}},
	)
	if rep := buildDinDPortReport(clean, []string{"acme", "globex"}, []localSlot{{"acme", "1"}, {"globex", "1"}}, never, "32768\t60999\n"); len(rep.Warnings) != 0 {
		t.Fatalf("clean layout warned: %v", rep.Warnings)
	}

	// A pool inside the default range, plus two unpartitioned slots of another org.
	mixed := portCfg(
		config.OrgConfig{Name: "acme", DinD: config.OrgDinD{PortRange: "40000-49999"}},
		config.OrgConfig{Name: "globex"},
	)
	rep := buildDinDPortReport(mixed, []string{"acme", "globex"}, []localSlot{{"acme", "1"}, {"globex", "1"}, {"globex", "2"}}, never, "32768\t60999\n")
	joined := strings.Join(rep.Warnings, "\n")
	for _, want := range []string{
		"2 DinD slot(s) of globex have no dind.portRange",
		"org acme: pool 40000-49999 overlaps the default range",
		"org acme: pool 40000-49999 overlaps the host's ephemeral range 32768-60999",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing warning %q in:\n%s", want, joined)
		}
	}

	// Overlapping pools between orgs surface the config-level issue.
	overlap := portCfg(
		config.OrgConfig{Name: "acme", DinD: config.OrgDinD{PortRange: "10000-20999"}},
		config.OrgConfig{Name: "globex", DinD: config.OrgDinD{PortRange: "20000-29999"}},
	)
	rep = buildDinDPortReport(overlap, []string{"acme", "globex"}, nil, never, "")
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "overlap") {
		t.Fatalf("overlap warning missing: %v", rep.Warnings)
	}
	// Scoped to one unrelated org (doctor --org), the other orgs' issue is not shown.
	if rep := buildDinDPortReport(portCfg(append(overlap.Orgs, config.OrgConfig{Name: "initech"})...), []string{"initech"}, nil, never, ""); len(rep.Warnings) != 0 {
		t.Fatalf("issue for other orgs leaked into a scoped report: %v", rep.Warnings)
	}
}
