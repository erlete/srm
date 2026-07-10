package cli

import (
	"strings"
	"testing"

	"github.com/erlete/srm/internal/runner"
)

// TestSubIDRangeCount guards the doctor subuid/subgid probe against the substring trap
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
		got := usernsReadiness(c.clone, c.maxns)
		enabled := strings.HasPrefix(got, "enabled")
		if enabled != c.wantEnabled {
			t.Errorf("usernsReadiness(clone=%q, maxns=%q) = %q; enabled=%v want %v", c.clone, c.maxns, got, enabled, c.wantEnabled)
		}
	}
}
