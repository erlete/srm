package tui

import (
	"strings"
	"testing"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

func TestSparkline(t *testing.T) {
	cases := []struct {
		name    string
		samples []int64
		max     int64
		want    string
	}{
		{"empty", nil, 100, ""},
		{"scaled to cap", []int64{0, 50, 100}, 100, "▁▄█"},
		{"clamped over cap", []int64{200}, 100, "█"},
		{"autoscale when no cap", []int64{1, 2, 4}, 0, "▂▄█"},
		{"all zero no cap", []int64{0, 0}, 0, "▁▁"},
		{"negative treated as zero", []int64{-5, 100}, 100, "▁█"},
	}
	for _, c := range cases {
		if got := sparkline(c.samples, c.max); got != c.want {
			t.Errorf("%s: sparkline(%v, %d) = %q, want %q", c.name, c.samples, c.max, got, c.want)
		}
	}
}

// recordMemSamples grows a per-runner ring on each host-tier merge, bounds it to
// memHistoryMax, skips unknown (-1) readings, and prunes rings for vanished runners.
func TestRecordMemSamples(t *testing.T) {
	m := sizedModel(t)
	runner := func(name string, mem int64, known bool) service.FusedRunner {
		return service.FusedRunner{
			RunnerWithOrg: service.RunnerWithOrg{Org: "acme", Runner: core.Runner{Name: name}},
			MemCur:        mem, HostKnown: known,
		}
	}

	// Two merges accumulate two samples for the audited runner; the unaudited one
	// (HostKnown=false) records nothing.
	m.snap = service.FleetSnapshot{Runners: []service.FusedRunner{runner("r-1", 1<<30, true), runner("r-2", 0, false)}}
	m.recordMemSamples()
	m.snap = service.FleetSnapshot{Runners: []service.FusedRunner{runner("r-1", 2<<30, true), runner("r-2", 0, false)}}
	m.recordMemSamples()

	if h := m.memHistoryFor(memKeyRunner(runner("r-1", 0, true))); len(h) != 2 || h[0] != 1<<30 || h[1] != 2<<30 {
		t.Fatalf("r-1 history = %v, want two accumulating samples", h)
	}
	if h := m.memHistoryFor(memKeyRunner(runner("r-2", 0, false))); len(h) != 0 {
		t.Errorf("unaudited r-2 should record no samples, got %v", h)
	}

	// The ring is bounded to the last memHistoryMax samples.
	for i := range memHistoryMax + 10 {
		m.snap = service.FleetSnapshot{Runners: []service.FusedRunner{runner("r-1", int64(i), true)}}
		m.recordMemSamples()
	}
	if h := m.memHistoryFor(memKeyRunner(runner("r-1", 0, true))); len(h) != memHistoryMax {
		t.Errorf("ring not bounded: len = %d, want %d", len(h), memHistoryMax)
	}

	// r-1 vanishing from the snapshot prunes its ring.
	m.snap = service.FleetSnapshot{Runners: []service.FusedRunner{runner("r-9", 5, true)}}
	m.recordMemSamples()
	if h := m.memHistoryFor(memKeyRunner(runner("r-1", 0, true))); h != nil {
		t.Errorf("vanished r-1 history should be pruned, got %v", h)
	}
}

// memSection appends the sparkline row only once there are >= 2 samples; a single
// sample (no trend) leaves the section at cgroup + oom.
func TestMemSectionSparklineGate(t *testing.T) {
	th := NewTheme()
	if rows := memSection(th, "live 1G", 0, []int64{1 << 30}, 4<<30); len(rows) != 2 {
		t.Errorf("one sample should not add a sparkline row, got %d rows", len(rows))
	}
	rows := memSection(th, "live 1G", 0, []int64{1 << 30, 2 << 30}, 4<<30)
	if len(rows) != 3 || rows[2][0] != "trend" {
		t.Fatalf("two samples should add a 'trend' sparkline row, got %v", rows)
	}
	if !strings.ContainsAny(rows[2][1], string(sparkRunes)) {
		t.Errorf("sparkline row value has no block runes: %q", rows[2][1])
	}
	if !strings.Contains(rows[2][1], "last 2 samples") {
		t.Errorf("sparkline row should caption the sample count, got %q", rows[2][1])
	}
}
