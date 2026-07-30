package service

import (
	"testing"

	"github.com/erlete/srm/internal/runner"
)

// TestClassifyPersistent locks the ORDER-SENSITIVE drift classification, above all
// the safety invariant that a NEWER-generation drop-in is authoritative-skipped
// (ClassDropInNewer) rather than refreshed back down - even though it also fails
// conformance (DropInOK=false) and may be inactive. A reorder of the switch would
// silently reintroduce the mixed-version ping-pong this guards against.
func TestClassifyPersistent(t *testing.T) {
	cases := []struct {
		name                 string
		insp                 runner.Inspection
		on, listedOK, online bool
		want                 string
	}{
		{"unknown: not on github + org list failed", runner.Inspection{HasTree: true}, false, false, false, ClassUnknown},
		{"orphan: not on github", runner.Inspection{HasTree: true}, false, true, false, ClassOrphanUnit},
		{"legacy flat tree", runner.Inspection{HasFlatTree: true, HasTree: false}, true, true, false, ClassLegacyFlat},
		{"NEWER marker wins over stale+inactive", runner.Inspection{HasTree: true, DropInNewer: true, DropInOK: false, DropInVer: 99, Active: false}, true, true, false, ClassDropInNewer},
		{"stale: older/non-conformant drop-in", runner.Inspection{HasTree: true, DropInOK: false}, true, true, true, ClassStaleDropIn},
		{"stuck: conformant but inactive", runner.Inspection{HasTree: true, DropInOK: true, Active: false}, true, true, true, ClassStuck},
		{"stuck: active but offline", runner.Inspection{HasTree: true, DropInOK: true, Active: true}, true, true, false, ClassStuck},
		{"healthy", runner.Inspection{HasTree: true, DropInOK: true, Active: true}, true, true, true, ClassHealthy},
	}
	for _, c := range cases {
		if got, _ := classifyPersistent(c.insp, c.on, c.listedOK, c.online); got != c.want {
			t.Errorf("%s: classifyPersistent = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestClassifyEphemeral locks the ephemeral classification, including that UnitNewer
// is authoritative-skipped before the down/drift cases (mirroring the persistent
// path) and - the regression guard for item 6 #4 - that a busy healthy lane with a
// high restart count is NOT false-flagged as crash-looping (Restart=always churns one
// restart per job, so NRestarts is not a crash signal).
func TestClassifyEphemeral(t *testing.T) {
	cases := []struct {
		name string
		insp runner.EphemeralInspection
		want string
	}{
		{"healthy active lane", runner.EphemeralInspection{Active: true, Restarts: 0, UnitOK: true, LastResult: "success"}, ClassEphemeralSlot},
		{"busy healthy lane, high restarts is NOT crash-loop", runner.EphemeralInspection{Active: true, UnitOK: true, Restarts: 999, LastResult: "success"}, ClassEphemeralSlot},
		{"NEWER marker wins over down/drift", runner.EphemeralInspection{Active: false, UnitNewer: true, UnitOK: false, UnitVer: 99}, ClassEphemeralNewer},
		{"down: inactive, clean last cycle", runner.EphemeralInspection{Active: false, UnitOK: true, LastResult: "success"}, ClassEphemeralStuck},
		{"crash-loop: inactive AND last cycle failed", runner.EphemeralInspection{Active: false, UnitOK: true, LastResult: "exit-code"}, ClassEphemeralStuck},
		{"unit-drift: non-conformant unit", runner.EphemeralInspection{Active: true, UnitOK: false}, ClassEphemeralStuck},
	}
	for _, c := range cases {
		if got, _ := classifyEphemeral(c.insp); got != c.want {
			t.Errorf("%s: classifyEphemeral = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestEphemeralSlotState locks the shared sub-state classifier - the single source
// the Ephemeral tab, the Drift tab, and reconcile's detail all read (item 6 #4/#14).
func TestEphemeralSlotState(t *testing.T) {
	cases := []struct {
		name           string
		active, unitOK bool
		lastResult     string
		wantLabel      string
		wantHealthy    bool
	}{
		{"active + conformant", true, true, "success", EphemeralActive, true},
		{"active + high churn still healthy", true, true, "", EphemeralActive, true},
		{"unit drifted (even while active)", true, false, "", EphemeralUnitDrift, false},
		{"down: inactive, clean", false, true, "success", EphemeralDown, false},
		{"down: inactive, result unknown", false, true, "", EphemeralDown, false},
		{"crash-loop: inactive + failed", false, true, "signal", EphemeralCrashLoop, false},
	}
	for _, c := range cases {
		gotLabel, gotHealthy := EphemeralSlotState(c.active, c.unitOK, c.lastResult)
		if gotLabel != c.wantLabel || gotHealthy != c.wantHealthy {
			t.Errorf("%s: EphemeralSlotState = (%q,%v), want (%q,%v)", c.name, gotLabel, gotHealthy, c.wantLabel, c.wantHealthy)
		}
	}
}

// TestClassLabel locks the canonical class->label vocab shared by the CLI and the
// TUI badges (item 6 #5), including that both stuck families collapse to one word and
// an unknown class echoes back verbatim.
func TestClassLabel(t *testing.T) {
	cases := map[string]string{
		ClassHealthy:        "ok",
		ClassEphemeralSlot:  "ok",
		ClassStaleDropIn:    "stale",
		ClassDropInNewer:    "newer",
		ClassEphemeralNewer: "newer",
		ClassStuck:          "stuck",
		ClassEphemeralStuck: "stuck",
		ClassOrphanUnit:     "orphan",
		ClassOrphanGitHub:   "ghost",
		ClassLegacyFlat:     "legacy",
		ClassUnknown:        "unknown",
		"":                  "unaudited",
		"made-up-class":     "made-up-class",
	}
	for class, want := range cases {
		if got := ClassLabel(class); got != want {
			t.Errorf("ClassLabel(%q) = %q, want %q", class, got, want)
		}
	}
}

// TestParseUnitName covers the systemd-unit → (org, name) split that drives orphan
// detection, including the live orphan units and prefix-collision safety.
func TestParseUnitName(t *testing.T) {
	// Caller passes orgs sorted longest-first.
	orgs := []string{"Globex", "Acme", "AB", "A"}

	cases := []struct {
		svc            string
		wantOrg, wantN string
		wantOK         bool
	}{
		{"actions.runner.Acme.temporal-1.service", "Acme", "temporal-1", true},
		{"actions.runner.Globex.runner-host-1774653292-1.service", "Globex", "runner-host-1774653292-1", true},
		{"actions.runner.AB.x.service", "AB", "x", true},    // longest-prefix wins over "A"
		{"actions.runner.A.y.service", "A", "y", true},      // shorter org still matches
		{"actions.runner.Unknown.z.service", "", "", false}, // not a configured org
		{"some-other-unit.service", "", "", false},          // not a runner unit
		{"actions.runner.Acme.service", "", "", false},      // no name segment
	}
	for _, c := range cases {
		org, name, ok := parseUnitName(c.svc, orgs)
		if ok != c.wantOK || org != c.wantOrg || name != c.wantN {
			t.Errorf("parseUnitName(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.svc, org, name, ok, c.wantOrg, c.wantN, c.wantOK)
		}
	}
}

// TestParseEphemeralUnitName covers the ephemeral slot unit split, including not
// matching a PERSISTENT runner unit (the two families must never cross over) and
// the same longest-prefix-wins org disambiguation as parseUnitName.
func TestParseEphemeralUnitName(t *testing.T) {
	orgs := []string{"Globex", "Acme", "AB", "A"} // longest-first

	cases := []struct {
		svc               string
		wantOrg, wantSlot string
		wantOK            bool
	}{
		{"actions.ephemeral.Acme.3.service", "Acme", "3", true},
		{"actions.ephemeral.Globex.1.service", "Globex", "1", true},
		{"actions.ephemeral.AB.x.service", "AB", "x", true},       // longest-prefix wins over "A"
		{"actions.runner.Acme.temporal-1.service", "", "", false}, // PERSISTENT unit - not ephemeral
		{"actions.ephemeral.Unknown.1.service", "", "", false},    // not a configured org
		{"actions.ephemeral.Acme.service", "", "", false},         // no slot segment
		{"some-other-unit.service", "", "", false},                // unrelated
	}
	for _, c := range cases {
		org, slot, ok := parseEphemeralUnitName(c.svc, orgs)
		if ok != c.wantOK || org != c.wantOrg || slot != c.wantSlot {
			t.Errorf("parseEphemeralUnitName(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.svc, org, slot, ok, c.wantOrg, c.wantSlot, c.wantOK)
		}
	}
}
