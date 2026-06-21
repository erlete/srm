package service

import (
	"strings"
	"testing"
)

// TestIDMismatchSkip locks the host-bound id gate: it fires ONLY when both ids are
// known and disagree, and is otherwise a no-op (an unknown id cannot gate, since the
// manifest is advisory).
func TestIDMismatchSkip(t *testing.T) {
	cases := []struct {
		name           string
		recorded, live int64
		wantSkip       bool
	}{
		{"both known, equal", 42, 42, false},
		{"both known, differ -> skip", 42, 99, true},
		{"recorded unknown", 0, 99, false},
		{"live unknown", 42, 0, false},
		{"both unknown", 0, 0, false},
	}
	for _, c := range cases {
		got := idMismatchSkip(c.recorded, c.live)
		if (got != "") != c.wantSkip {
			t.Errorf("%s: idMismatchSkip(%d,%d) = %q, wantSkip=%v", c.name, c.recorded, c.live, got, c.wantSkip)
		}
	}
}

// TestVersionGateSkip locks the ORDER-SENSITIVE version decision and every override.
func TestVersionGateSkip(t *testing.T) {
	cases := []struct {
		name            string
		from, to        string
		force, rollback bool
		wantSkip        bool
		wantContains    string
	}{
		{"forward upgrade proceeds", "2.334.0", "2.335.1", false, false, false, ""},
		{"already at target -> skip", "2.335.1", "2.335.1", false, false, true, "already at"},
		{"downgrade -> skip", "2.335.1", "2.334.0", false, false, true, "older than"},
		{"downgrade with --force proceeds", "2.335.1", "2.334.0", true, false, false, ""},
		{"downgrade as rollback proceeds", "2.335.1", "2.334.0", false, true, false, ""},
		{"rollback to same version still skips at-target", "2.335.1", "2.335.1", false, true, true, "already at"},
		{"unknown installed version is always eligible", "", "2.335.1", false, false, false, ""},
		{"force re-install at same version proceeds", "2.335.1", "2.335.1", true, false, false, ""},
		// at-target is checked BEFORE downgrade: equal versions report "already at",
		// never the downgrade message (they aren't a downgrade anyway, but this pins order).
		{"equal versions report at-target not downgrade", "2.335.1", "2.335.1", false, false, true, "already at"},
	}
	for _, c := range cases {
		got := versionGateSkip(c.from, c.to, c.force, c.rollback)
		if (got != "") != c.wantSkip {
			t.Errorf("%s: versionGateSkip(%q,%q,force=%v,rb=%v) = %q, wantSkip=%v", c.name, c.from, c.to, c.force, c.rollback, got, c.wantSkip)
			continue
		}
		if c.wantContains != "" && !strings.Contains(got, c.wantContains) {
			t.Errorf("%s: skip reason %q does not contain %q", c.name, got, c.wantContains)
		}
	}
}
