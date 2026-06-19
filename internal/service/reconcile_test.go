package service

import "testing"

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
		{"actions.runner.Acme.service", "", "", false},  // no name segment
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
		{"actions.ephemeral.AB.x.service", "AB", "x", true},           // longest-prefix wins over "A"
		{"actions.runner.Acme.temporal-1.service", "", "", false}, // PERSISTENT unit — not ephemeral
		{"actions.ephemeral.Unknown.1.service", "", "", false},        // not a configured org
		{"actions.ephemeral.Acme.service", "", "", false},         // no slot segment
		{"some-other-unit.service", "", "", false},                    // unrelated
	}
	for _, c := range cases {
		org, slot, ok := parseEphemeralUnitName(c.svc, orgs)
		if ok != c.wantOK || org != c.wantOrg || slot != c.wantSlot {
			t.Errorf("parseEphemeralUnitName(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.svc, org, slot, ok, c.wantOrg, c.wantSlot, c.wantOK)
		}
	}
}
