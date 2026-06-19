package service

import (
	"testing"

	"github.com/erlete/srm/internal/core"
)

func TestValidateLabels(t *testing.T) {
	if err := ValidateLabels(nil); err == nil {
		t.Fatal("expected error for zero labels")
	}
	if err := ValidateLabels([]string{"a"}); err != nil {
		t.Fatalf("unexpected error for one label: %v", err)
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = "x"
	}
	if err := ValidateLabels(many); err == nil {
		t.Fatal("expected error for >100 labels")
	}
}

func TestOfflineRunners(t *testing.T) {
	in := []core.Runner{
		{Name: "a", Status: core.StatusOnline},
		{Name: "b", Status: core.StatusOffline},
		{Name: "c", Status: core.StatusOffline},
	}
	got := OfflineRunners(in)
	if len(got) != 2 {
		t.Fatalf("got %d offline, want 2", len(got))
	}
}
