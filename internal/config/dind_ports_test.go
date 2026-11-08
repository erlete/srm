package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestParsePortRange(t *testing.T) {
	good := map[string][2]int{
		"10000-29999":   {10000, 29999},
		" 1024 - 1024 ": {1024, 1024},
		"61000-65535":   {61000, 65535},
	}
	for in, want := range good {
		lo, hi, err := ParsePortRange(in)
		if err != nil || lo != want[0] || hi != want[1] {
			t.Errorf("ParsePortRange(%q) = %d, %d, %v; want %d, %d", in, lo, hi, err, want[0], want[1])
		}
	}
	for _, in := range []string{"", "10000", "a-b", "10000-x", "1023-2000", "2000-1999", "60000-65536", "-5-10"} {
		if _, _, err := ParsePortRange(in); err == nil {
			t.Errorf("ParsePortRange(%q) accepted an invalid range", in)
		}
	}
}

func TestSlotPortRangeBlocks(t *testing.T) {
	d := OrgDinD{PortRange: "10000-29999", PortsPerSlot: 500}
	cases := map[string][2]int{
		"1":  {10000, 10499},
		"2":  {10500, 10999},
		"27": {23000, 23499},
		"40": {29500, 29999}, // last block that fits
	}
	for slot, want := range cases {
		lo, hi, err := d.SlotPortRange(slot)
		if err != nil || lo != want[0] || hi != want[1] {
			t.Errorf("slot %s = %d-%d, %v; want %d-%d", slot, lo, hi, err, want[0], want[1])
		}
	}
	if n, err := d.SlotCapacity(); err != nil || n != 40 {
		t.Errorf("SlotCapacity = %d, %v; want 40", n, err)
	}
	if _, _, err := d.SlotPortRange("41"); err == nil || !strings.Contains(err.Error(), "slots 1-40") {
		t.Errorf("slot 41 should not fit and name the capacity, got %v", err)
	}
	for _, bad := range []string{"0", "-1", "x", ""} {
		if _, _, err := d.SlotPortRange(bad); err == nil {
			t.Errorf("slot %q should be rejected", bad)
		}
	}
}

func TestSlotPortRangeDefaultsAndErrors(t *testing.T) {
	d := OrgDinD{PortRange: "40000-49999"} // PortsPerSlot 0 -> default
	lo, hi, err := d.SlotPortRange("3")
	if err != nil || lo != 40000+2*DefaultDinDPortsPerSlot || hi != lo+DefaultDinDPortsPerSlot-1 {
		t.Fatalf("default block size not applied: %d-%d, %v", lo, hi, err)
	}
	if _, _, err := (OrgDinD{}).SlotPortRange("1"); err == nil {
		t.Error("partitioning off must error, not return a range")
	}
	if _, _, err := (OrgDinD{PortRange: "40000-40099", PortsPerSlot: 101}).SlotPortRange("1"); err == nil {
		t.Error("a block larger than the pool must be rejected")
	}
	if _, _, err := (OrgDinD{PortRange: "40000-40099", PortsPerSlot: -5}).SlotPortRange("1"); err == nil {
		t.Error("a negative block size must be rejected")
	}
}

// Adjacent slots never share a port, so two rootless daemons on one host can no
// longer hand out the same dynamic host port.
func TestSlotPortRangesDisjoint(t *testing.T) {
	d := OrgDinD{PortRange: "10000-29999", PortsPerSlot: 500}
	prevHi := 0
	for n := 1; n <= 40; n++ {
		lo, hi, err := d.SlotPortRange(strconv.Itoa(n))
		if err != nil {
			t.Fatal(err)
		}
		if lo <= prevHi {
			t.Fatalf("slot %d (%d-%d) overlaps the previous slot (ended at %d)", n, lo, hi, prevHi)
		}
		prevHi = hi
	}
}

func TestDinDPortIssues(t *testing.T) {
	c := &Config{Orgs: []OrgConfig{
		{Name: "acme", DinD: OrgDinD{PortRange: "10000-29999", PortsPerSlot: 500}},
		{Name: "globex", DinD: OrgDinD{PortRange: "30000-31999", PortsPerSlot: 500}},
		{Name: "plain"}, // partitioning off: never an issue
	}}
	if got := c.DinDPortIssues(); len(got) != 0 {
		t.Fatalf("disjoint pools reported issues: %v", got)
	}

	c.Orgs[1].DinD.PortRange = "29000-31999"
	got := c.DinDPortIssues()
	if len(got) != 1 || !strings.Contains(got[0], "acme and globex") || !strings.Contains(got[0], "overlap") {
		t.Fatalf("overlap not reported as one acme/globex issue: %v", got)
	}

	c.Orgs[1].DinD.PortRange = "nope"
	got = c.DinDPortIssues()
	if len(got) != 1 || !strings.Contains(got[0], "org globex") {
		t.Fatalf("malformed pool not reported: %v", got)
	}
}

// The dind block round-trips through config.yaml and is omitted when unset, so an
// existing config file without it loads and re-saves unchanged.
func TestLoadOrgDinD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yml := "orgs:\n  - name: acme\n    appID: 1\n    installationID: 2\n    dind:\n      portRange: 10000-29999\n      portsPerSlot: 500\n  - name: globex\n    appID: 3\n    installationID: 4\n"
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	acme, _ := c.Org("acme")
	if acme.DinD.PortRange != "10000-29999" || acme.DinD.PortsPerSlot != 500 {
		t.Fatalf("acme dind not loaded: %+v", acme.DinD)
	}
	globex, _ := c.Org("globex")
	if !globex.DinD.IsZero() || globex.DinD.PortsEnabled() {
		t.Fatalf("globex should have no dind settings: %+v", globex.DinD)
	}
}
