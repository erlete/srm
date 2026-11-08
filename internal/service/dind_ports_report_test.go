package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/runner"
)

func portCfg(orgs ...config.OrgConfig) *config.Config { return &config.Config{Orgs: orgs} }

func TestDinDPortReportStates(t *testing.T) {
	cfg := portCfg(
		config.OrgConfig{Name: "acme", DinD: config.OrgDinD{PortRange: "10000-10999", PortsPerSlot: 500}},
		config.OrgConfig{Name: "globex"},
	)
	slots := []localSlot{{"acme", "2"}, {"acme", "1"}, {"acme", "3"}, {"acme", "4"}, {"globex", "1"}}
	eff := map[string][2]int{
		"acme/1":   {10000, 10499}, // already on its block
		"acme/2":   {32768, 60999}, // daemon predates the change
		"acme/3":   {32768, 60999}, // slot 3 does not fit a 2-slot pool
		"globex/1": {32768, 60999},
	}
	read := func(org, slot string) (int, int, error) {
		if r, ok := eff[org+"/"+slot]; ok {
			return r[0], r[1], nil
		}
		return 0, 0, runner.ErrDinDNotRunning
	}
	rep := buildDinDPortReport(cfg, []string{"acme", "globex"}, slots, read, "")
	if !rep.Enabled || !rep.AnyPartitioned() {
		t.Fatalf("report not enabled/partitioned: %+v", rep)
	}
	want := map[string]string{
		"acme/1":   PortStatePinned,
		"acme/2":   PortStatePending,
		"acme/3":   PortStateNotApplied,
		"acme/4":   PortStateNotApplied,
		"globex/1": PortStateDefault,
	}
	var order []string
	for _, s := range rep.Slots {
		order = append(order, s.Org+"/"+s.Slot)
		if s.State != want[s.Org+"/"+s.Slot] {
			t.Errorf("%s/%s state %q, want %q (%+v)", s.Org, s.Slot, s.State, want[s.Org+"/"+s.Slot], s)
		}
	}
	if strings.Join(order, ",") != "acme/1,acme/2,acme/3,acme/4,globex/1" {
		t.Errorf("slots not ordered by org then slot number: %v", order)
	}
	if rep.Orgs[0].Pool != "10000-10999" || rep.Orgs[0].Capacity != 2 || rep.Orgs[1].Partitioned {
		t.Errorf("org pools wrong: %+v", rep.Orgs)
	}
}

func TestDinDPortReportNotRunningAndUnreadable(t *testing.T) {
	cfg := portCfg(config.OrgConfig{Name: "acme", DinD: config.OrgDinD{PortRange: "10000-19999"}})
	read := func(_, slot string) (int, int, error) {
		if slot == "1" {
			return 0, 0, runner.ErrDinDNotRunning
		}
		return 0, 0, errors.New("needs root to enter the slot's network namespace")
	}
	rep := buildDinDPortReport(cfg, []string{"acme"}, []localSlot{{"acme", "1"}, {"acme", "2"}}, read, "")
	if rep.Slots[0].State != PortStateNotRunning || rep.Slots[1].State != PortStateUnreadable {
		t.Fatalf("states = %q, %q", rep.Slots[0].State, rep.Slots[1].State)
	}
}

func TestDinDPortReportWarnings(t *testing.T) {
	never := func(string, string) (int, int, error) { return 0, 0, runner.ErrDinDNotRunning }

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
