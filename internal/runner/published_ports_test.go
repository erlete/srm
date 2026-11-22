package runner

import (
	"errors"
	"testing"

	"github.com/erlete/srm/internal/config"
)

func TestPublishedPortEnv(t *testing.T) {
	pool := config.OrgDinD{PortRange: "10000-19999", PortsPerSlot: 500}
	onBlock := func(lo, hi int) func() (DinDNetns, error) {
		return func() (DinDNetns, error) { return DinDNetns{PortLo: lo, PortHi: hi, TWReuse: 1}, nil }
	}
	// Slot 3 owns the third block.
	if got := publishedPortEnv(pool, "3", onBlock(11000, 11499)); got != "SRM_PUBLISHED_PORT_RANGE=11000-11499" {
		t.Errorf("pinned slot: %q", got)
	}
	// The daemon fell back to the default range (fail-open wrapper): say nothing.
	if got := publishedPortEnv(pool, "3", onBlock(32768, 60999)); got != "" {
		t.Errorf("fallback slot: %q", got)
	}
	if got := publishedPortEnv(pool, "3", func() (DinDNetns, error) { return DinDNetns{}, errors.New("no netns") }); got != "" {
		t.Errorf("unreadable namespace: %q", got)
	}
	// Org without a pool, and a slot past the pool's end.
	if got := publishedPortEnv(config.OrgDinD{}, "1", onBlock(10000, 10499)); got != "" {
		t.Errorf("no pool: %q", got)
	}
	if got := publishedPortEnv(pool, "99", onBlock(10000, 10499)); got != "" {
		t.Errorf("slot outside the pool: %q", got)
	}
}
