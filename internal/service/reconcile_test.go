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

// TestClassifyEphemeral locks the ephemeral classification order, including that
// UnitNewer is authoritative-skipped before the inactive/drift cases (mirroring the
// persistent path).
func TestClassifyEphemeral(t *testing.T) {
	cases := []struct {
		name string
		insp runner.EphemeralInspection
		want string
	}{
		{"healthy active lane", runner.EphemeralInspection{Active: true, Restarts: 0, UnitOK: true}, ClassEphemeralSlot},
		{"NEWER marker wins over inactive/drift", runner.EphemeralInspection{Active: false, UnitNewer: true, UnitOK: false, UnitVer: 99}, ClassEphemeralNewer},
		{"inactive", runner.EphemeralInspection{Active: false, UnitOK: true}, ClassEphemeralStuck},
		{"drifted: older/non-conformant", runner.EphemeralInspection{Active: true, UnitOK: false}, ClassEphemeralStuck},
		{"crash-looping", runner.EphemeralInspection{Active: true, UnitOK: true, Restarts: EphemeralRestartThreshold + 1}, ClassEphemeralStuck},
	}
	for _, c := range cases {
		if got, _ := classifyEphemeral(c.insp); got != c.want {
			t.Errorf("%s: classifyEphemeral = %q, want %q", c.name, got, c.want)
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
