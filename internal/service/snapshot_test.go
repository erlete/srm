package service

import (
	"testing"
	"time"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/runner"
)

// auditAt is a fixed audit timestamp for the merge tests (MergeHostTier stamps it
// onto the snapshot; the value itself is arbitrary but deterministic).
var auditAt = time.Unix(1_700_000_000, 0)

// fastSnap builds a fast-tier snapshot by hand (no Manager / no network), so the
// merge + count logic can be exercised deterministically.
func fastSnap() FleetSnapshot {
	return FleetSnapshot{
		PartialErrs: map[string]error{},
		Runners: []FusedRunner{
			{
				RunnerWithOrg: RunnerWithOrg{Org: "acme", Local: true, Runner: core.Runner{ID: 1, Name: "r-1", Status: "online"}},
				AgentVersion:  "2.334.0", Published: "2.335.1", Behind: true,
				MemCur: -1, MemPeak: -1, MemMax: -1, OOMKills: -1,
			},
			{
				RunnerWithOrg: RunnerWithOrg{Org: "acme", Local: true, Runner: core.Runner{ID: 2, Name: "r-2", Status: "online"}},
				AgentVersion:  "2.335.1", Published: "2.335.1",
				MemCur: -1, MemPeak: -1, MemMax: -1, OOMKills: -1,
			},
		},
		Slots: []FusedSlot{
			{EphemeralSlot: EphemeralSlot{Org: "acme", Slot: "1", Active: true, UnitOK: true}},
		},
	}
}

func TestMergeHostTierPopulatesDriftAndMemory(t *testing.T) {
	snap := fastSnap()
	rep := ReconcileReport{
		Runners: []RunnerState{
			{Org: "acme", Name: "r-1", Class: ClassStaleDropIn, Detail: "drop-in differs", MemCur: 1 << 30, MemPeak: 2 << 30, MemMax: 4 << 30, OOMKills: 0},
			{Org: "acme", Name: "r-2", Class: ClassHealthy, MemCur: 1 << 29, MemPeak: 1 << 30, MemMax: 4 << 30, OOMKills: 0},
			{Org: "acme", Name: "1", Class: ClassEphemeralSlot, Detail: "ephemeral slot healthy"},
		},
		SliceCurrent: 12 << 30, SliceMax: 16 << 30,
	}

	got := MergeHostTier(snap, rep, auditAt)

	if !got.HostTierAvailable {
		t.Fatal("HostTierAvailable should be true after a merge")
	}
	if !got.HostTierAt.Equal(auditAt) {
		t.Errorf("HostTierAt = %v, want the passed audit time %v", got.HostTierAt, auditAt)
	}
	if !got.Host.Loaded || got.Host.SliceMax != 16<<30 {
		t.Fatalf("host health not merged: %+v", got.Host)
	}
	r1 := got.Runners[0]
	if r1.DriftClass != ClassStaleDropIn || !r1.HostKnown {
		t.Errorf("r-1 drift not merged: class=%q hostKnown=%v", r1.DriftClass, r1.HostKnown)
	}
	if r1.MemPeak != 2<<30 || r1.MemMax != 4<<30 {
		t.Errorf("r-1 memory not merged: peak=%d max=%d", r1.MemPeak, r1.MemMax)
	}
	if got.Slots[0].DriftClass != ClassEphemeralSlot {
		t.Errorf("slot drift not merged: %q", got.Slots[0].DriftClass)
	}
}

func TestMergeHostTierLeavesUnmatchedUnaudited(t *testing.T) {
	snap := fastSnap()
	// A report that knows only r-1; r-2 must stay unaudited (no false healthy).
	rep := ReconcileReport{Runners: []RunnerState{{Org: "acme", Name: "r-1", Class: ClassHealthy}}}
	got := MergeHostTier(snap, rep, auditAt)
	if got.Runners[1].HostKnown {
		t.Error("r-2 should remain unaudited (absent from the report)")
	}
	if got.Runners[1].DriftClass != "" {
		t.Errorf("r-2 drift class should be empty, got %q", got.Runners[1].DriftClass)
	}
	if got.Runners[1].MemPeak != -1 {
		t.Errorf("r-2 memory should stay -1 sentinel, got %d", got.Runners[1].MemPeak)
	}
}

func TestDriftCountIgnoresUnauditedFastOnly(t *testing.T) {
	snap := fastSnap() // host tier never merged
	if n := snap.DriftCount(); n != 0 {
		t.Errorf("fast-only snapshot must report 0 drift (unaudited != drifted), got %d", n)
	}
}

func TestDriftAndNewerCountsAfterMerge(t *testing.T) {
	snap := fastSnap()
	rep := ReconcileReport{Runners: []RunnerState{
		{Org: "acme", Name: "r-1", Class: ClassDropInNewer},
		{Org: "acme", Name: "r-2", Class: ClassHealthy},
		{Org: "acme", Name: "1", Class: ClassEphemeralStuck},
	}}
	got := MergeHostTier(snap, rep, auditAt)
	if n := got.NewerOnDisk(); n != 1 {
		t.Errorf("NewerOnDisk = %d, want 1", n)
	}
	// r-1 (dropin-newer) + slot 1 (ephemeral-stuck) are non-healthy; r-2 is healthy.
	if n := got.DriftCount(); n != 2 {
		t.Errorf("DriftCount = %d, want 2", n)
	}
}

func TestBehindCount(t *testing.T) {
	snap := fastSnap()
	if n := snap.BehindCount(); n != 1 {
		t.Errorf("BehindCount = %d, want 1 (r-1 is behind)", n)
	}
}

func TestIsBehind(t *testing.T) {
	cases := []struct {
		installed, published string
		want                 bool
	}{
		{"2.334.0", "2.335.1", true},
		{"2.335.1", "2.335.1", false},
		{"2.336.0", "2.335.1", false}, // ahead, not behind
		{"", "2.335.1", false},        // unrecorded
		{"2.334.0", "", false},        // unresolved
	}
	for _, c := range cases {
		if got := isBehind(c.installed, c.published); got != c.want {
			t.Errorf("isBehind(%q,%q) = %v, want %v", c.installed, c.published, got, c.want)
		}
	}
	_ = runner.CompareVersions // keep the import meaningful if cases shrink
}
