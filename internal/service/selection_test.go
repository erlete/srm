package service

import (
	"testing"

	"github.com/erlete/srm/internal/core"
)

func TestScanConcurrency(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, 8}, {-1, 8}, {-100, 8}, {1, 1}, {4, 4}, {16, 16},
	} {
		if got := scanConcurrency(tc.in); got != tc.want {
			t.Errorf("scanConcurrency(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// OnlyRunners builds the upgrade/refresh selection filter; the keys must match
// RunnerRef so the service-side scope check finds the selected runners.
func TestOnlyRunners(t *testing.T) {
	if OnlyRunners(nil) != nil {
		t.Fatal("OnlyRunners(nil) should be nil (no restriction)")
	}
	if OnlyRunners([]FusedRunner{}) != nil {
		t.Fatal("OnlyRunners(empty) should be nil (no restriction)")
	}
	rs := []FusedRunner{
		{RunnerWithOrg: RunnerWithOrg{Org: "acme", Runner: core.Runner{Name: "a"}}},
		{RunnerWithOrg: RunnerWithOrg{Org: "beta", Runner: core.Runner{Name: "b"}}},
	}
	only := OnlyRunners(rs)
	if len(only) != 2 {
		t.Fatalf("len(only) = %d, want 2", len(only))
	}
	if !only[RunnerRef("acme", "a")] || !only[RunnerRef("beta", "b")] {
		t.Errorf("missing expected keys in %v", only)
	}
	if only[RunnerRef("acme", "b")] {
		t.Error("cross-org key should not match (org must be part of the key)")
	}
}
