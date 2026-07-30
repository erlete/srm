package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

// fixPlan maps every drift class to an operator remediation; unknown classes fall
// back to a dash so a new class never renders an empty cell.
func TestFixPlan(t *testing.T) {
	cases := map[string]string{
		service.ClassStaleDropIn:    "refresh drop-in",
		service.ClassStuck:          "restart",
		service.ClassOrphanUnit:     "remove orphan unit",
		service.ClassEphemeralStuck: "recreate lane",
		service.ClassDropInNewer:    "update srm binary",
		service.ClassEphemeralNewer: "update srm binary",
		service.ClassLegacyFlat:     "migrate (report)",
		service.ClassOrphanGitHub:   "report only",
		service.ClassUnknown:        "n/a (list failed)",
		"some-future-class":         "-",
	}
	for class, want := range cases {
		if got := fixPlan(class); got != want {
			t.Errorf("fixPlan(%q) = %q, want %q", class, got, want)
		}
	}
}

func TestHumanAgo(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-5 * time.Second, "0s ago"},
		{5 * time.Second, "5s ago"},
		{90 * time.Second, "1m ago"},
		{2 * time.Hour, "2h ago"},
	}
	for _, c := range cases {
		if got := humanAgo(c.d); got != c.want {
			t.Errorf("humanAgo(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// The provenance line degrades gracefully with no timestamp and otherwise names the
// audit age + that it was a read-only pass.
func TestDriftProvenance(t *testing.T) {
	if got := driftProvenance(time.Time{}); got != "read-only audit" {
		t.Errorf("zero-time provenance = %q, want %q", got, "read-only audit")
	}
	got := driftProvenance(time.Now().Add(-8 * time.Second))
	if !strings.Contains(got, "audited") || !strings.Contains(got, "read-only pass") {
		t.Errorf("provenance = %q, want it to name the audit + read-only pass", got)
	}
}

// setRows turns a drifted snapshot row into a table row whose FIX-PLAN cell is the
// class's remediation (end-to-end through the rendered table).
func TestDriftRowRendersFixPlan(t *testing.T) {
	v := newDriftView(NewTheme())
	v.setSize(120, 20)
	snap := service.FleetSnapshot{
		HostTierAvailable: true,
		Runners: []service.FusedRunner{{
			RunnerWithOrg: service.RunnerWithOrg{Org: "acme", Runner: core.Runner{Name: "r-1"}},
			DriftClass:    service.ClassStuck, DriftDetail: "dead", HostKnown: true,
		}},
	}
	v.setRows(snap)
	if len(v.rows) != 1 || v.rows[0].class != service.ClassStuck {
		t.Fatalf("expected one stuck drift row, got %+v", v.rows)
	}
	if !strings.Contains(v.tableView(), "restart") {
		t.Errorf("rendered drift table missing the FIX-PLAN cell 'restart':\n%s", v.tableView())
	}
}
