package tui

import (
	"errors"
	"testing"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

func TestUpgradeItemsOutcomeMapping(t *testing.T) {
	results := []service.UpgradeResult{
		{Kind: service.KindPersistent, Org: "acme", Name: "r-1", From: "2.334.0", To: "2.335.1", Upgraded: true},
		{Kind: service.KindPersistent, Org: "acme", Name: "r-2", Skipped: "busy (job running)"},
		{Kind: service.KindPersistent, Org: "acme", Name: "r-3", From: "2.334.0", RolledBack: true},
		{Kind: service.KindPersistent, Org: "acme", Name: "r-4", Err: errors.New("boom")},
		{Kind: service.KindEphemeral, Org: "acme", Name: "1", Skipped: "not supported"},
	}
	items := upgradeItems(results)
	if len(items) != 5 {
		t.Fatalf("got %d items, want 5", len(items))
	}
	wantOutcomes := []opOutcome{outcomeOK, outcomeSkip, outcomeRollback, outcomeFail, outcomeSkip}
	for i, want := range wantOutcomes {
		if items[i].Outcome != want {
			t.Errorf("item %d outcome = %v, want %v", i, items[i].Outcome, want)
		}
	}
	if items[0].Detail != "2.334.0 -> 2.335.1" {
		t.Errorf("upgrade detail = %q", items[0].Detail)
	}
	if items[4].Label != "acme/slot 1" {
		t.Errorf("ephemeral label = %q, want acme/slot 1", items[4].Label)
	}
}

func TestCustomLabelsDropsReadOnly(t *testing.T) {
	labels := []core.Label{
		{Name: "self-hosted", ReadOnly: true},
		{Name: "Linux", ReadOnly: true},
		{Name: "X64", ReadOnly: true},
		{Name: "temporal", ReadOnly: false},
		{Name: "gpu", ReadOnly: false},
	}
	got := customLabels(labels)
	if len(got) != 2 || got[0] != "temporal" || got[1] != "gpu" {
		t.Errorf("customLabels = %v, want [temporal gpu]", got)
	}
}
