package service

import (
	"testing"

	"github.com/erlete/srm/internal/runner"
)

// TestSubIDRangeCount guards the DinD subuid/subgid probe against the substring trap
// (a user whose name is a colon-suffix of another, "acme" vs "srm-acme", must NOT read
// off the other's range) and confirms it returns the COUNT so an undersized range is
// flagged rather than greened. Exact first-field match, mirroring ensureSubIDFile.
func TestSubIDRangeCount(t *testing.T) {
	file := "srm-acme:100000:65536\nsrm-globex:165536:65536\nsrm-tiny:300000:1000\n"
	cases := []struct {
		user string
		want int
	}{
		{"srm-acme", 65536},
		{"srm-globex", 65536},
		{"srm-tiny", 1000}, // undersized: doctor must report this, not green it
		{"acme", 0},        // suffix of srm-acme - must NOT false-match
		{"globex", 0},
		{"srm", 0},
		{"nobody", 0},
	}
	for _, c := range cases {
		if got := subIDRangeCount(file, c.user); got != c.want {
			t.Errorf("subIDRangeCount(_, %q) = %d, want %d", c.user, got, c.want)
		}
	}
	// An undersized range is below the allocator's width, so doctor flags it.
	if subIDRangeCount(file, "srm-tiny") >= runner.SubIDCount {
		t.Error("srm-tiny range must be detected as smaller than runner.SubIDCount")
	}
	// Malformed lines (wrong field count) are ignored, not matched.
	if subIDRangeCount("srm-acme:100000\nbad\n", "srm-acme") != 0 {
		t.Error("a 2-field line must not count as an allocated range")
	}
}

// TestDinDBlockers: only failed HARD checks are blockers; a failed soft check or any
// passing check is not, and AllOK stays independent of hardness.
func TestDinDBlockers(t *testing.T) {
	rep := DinDReport{Enabled: true, Checks: []DinDCheck{
		{Name: "docker", OK: true, Hard: true},
		{Name: "fuse-overlayfs", OK: false, Hard: true},   // blocker
		{Name: "newuidmap setuid", OK: false, Hard: true}, // blocker
		{Name: "some-advisory", OK: false, Hard: false},   // NOT a blocker
	}}
	if !rep.HasBlocker() {
		t.Fatal("a failed hard check must make HasBlocker true")
	}
	if got := len(rep.Blockers()); got != 2 {
		t.Fatalf("Blockers() = %d, want 2 (the failed hard checks only)", got)
	}
	if rep.AllOK() {
		t.Fatal("AllOK must be false when any check failed")
	}

	// No hard failures -> no blocker even if a soft check failed.
	soft := DinDReport{Enabled: true, Checks: []DinDCheck{
		{Name: "ok", OK: true, Hard: true},
		{Name: "advisory", OK: false, Hard: false},
	}}
	if soft.HasBlocker() {
		t.Fatal("a failed SOFT check must not be a blocker")
	}
	// A disabled report (DinD off) has no checks and no blocker.
	if (DinDReport{}).HasBlocker() {
		t.Fatal("a disabled report must have no blocker")
	}
}

// TestUsernsReadiness pins the fix: unprivileged_userns_clone=0 (present and zero)
// means DISABLED even when max_user_namespaces is a large positive default - the
// hardened-kernel combination the old probe false-greened.
func TestUsernsReadiness(t *testing.T) {
	cases := []struct {
		clone, maxns string
		wantEnabled  bool
	}{
		{"0", "15000", false}, // the bug: clone=0 blocks despite a positive maxns
		{"0", "", false},
		{"1", "0", true},
		{"1", "15000", true},
		{"", "15000", true}, // no clone knob, maxns positive
		{"", "0", false},
		{"", "", false},
	}
	for _, c := range cases {
		ok, detail := usernsReadiness(c.clone, c.maxns)
		if ok != c.wantEnabled {
			t.Errorf("usernsReadiness(clone=%q, maxns=%q) = (%v, %q); want enabled=%v", c.clone, c.maxns, ok, detail, c.wantEnabled)
		}
	}
}
