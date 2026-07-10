package tui

import (
	"errors"
	"testing"

	"github.com/erlete/srm/internal/service"
)

func TestDriftGlyphCoversClasses(t *testing.T) {
	cases := map[string]string{
		service.ClassHealthy:        "ok",
		service.ClassStaleDropIn:    "stale",
		service.ClassDropInNewer:    "newer",
		service.ClassEphemeralNewer: "newer",
		service.ClassStuck:          "stuck",
		service.ClassOrphanUnit:     "orphan",
		service.ClassOrphanGitHub:   "ghost",
		service.ClassLegacyFlat:     "legacy",
		service.ClassUnknown:        "unknown",
		"":                          "unaudited",
	}
	for class, wantLabel := range cases {
		g, l := driftGlyph(class)
		if l != wantLabel {
			t.Errorf("driftGlyph(%q) label = %q, want %q", class, l, wantLabel)
		}
		if g == "" {
			t.Errorf("driftGlyph(%q) returned empty glyph", class)
		}
	}
}

func TestFixResultItemsOutcomeMapping(t *testing.T) {
	rep := service.ReconcileReport{Runners: []service.RunnerState{
		{Org: "acme", Name: "r-1", Class: service.ClassStaleDropIn, Fix: "refreshed"},
		{Org: "acme", Name: "r-2", Class: service.ClassStuck, Fix: "skipped (busy)"},
		{Org: "acme", Name: "r-3", Class: service.ClassOrphanUnit, FixErr: errors.New("boom")},
		{Org: "acme", Name: "r-4", Class: service.ClassHealthy}, // no fix -> omitted
	}}
	items := fixResultItems(rep)
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 (healthy/no-fix omitted)", len(items))
	}
	if items[0].Outcome != outcomeOK {
		t.Errorf("r-1 should be ok, got %v", items[0].Outcome)
	}
	if items[1].Outcome != outcomeSkip {
		t.Errorf("r-2 should be skip, got %v", items[1].Outcome)
	}
	if items[2].Outcome != outcomeFail {
		t.Errorf("r-3 should be fail, got %v", items[2].Outcome)
	}
}

func TestReapResultItems(t *testing.T) {
	rep := service.ReconcileReport{Reaped: []service.RunnerState{
		{Org: "acme", Name: "srm-eph-x", Fix: "reaped (deregistered)"},
		{Org: "acme", Name: "srm-eph-y", FixErr: errors.New("nope")},
	}}
	items := reapResultItems(rep)
	if len(items) != 2 || items[0].Outcome != outcomeOK || items[1].Outcome != outcomeFail {
		t.Fatalf("unexpected reap items: %+v", items)
	}
}

func TestOpTally(t *testing.T) {
	items := []opItem{
		{Outcome: outcomeOK}, {Outcome: outcomeOK},
		{Outcome: outcomeSkip}, {Outcome: outcomeRollback}, {Outcome: outcomeFail},
	}
	got := opTally(items)
	want := "2 ok · 1 skipped · 1 rolled back · 1 failed"
	if got != want {
		t.Errorf("opTally = %q, want %q", got, want)
	}
	if opTally(nil) != "no items" {
		t.Errorf("empty tally = %q, want 'no items'", opTally(nil))
	}
}

func TestTypedConfirmGate(t *testing.T) {
	c := newTypedConfirm(NewTheme(), "Upgrade ALL", "type it", "UPGRADE ALL", "token")
	if c.matched() {
		t.Error("empty input should not match")
	}
	c.input.SetValue("UPGRADE ALL")
	if !c.matched() {
		t.Error("exact token should match")
	}
	c.input.SetValue("upgrade all")
	if c.matched() {
		t.Error("wrong case should not match")
	}
}
