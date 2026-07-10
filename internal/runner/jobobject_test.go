package runner

import (
	"testing"

	"github.com/erlete/srm/internal/config"
)

func TestParseSystemdBytes(t *testing.T) {
	const totalRAM = uint64(8) << 30 // 8 GiB
	cases := []struct {
		in      string
		total   uint64
		wantVal uint64
		wantOK  bool
	}{
		{"2G", totalRAM, 2 << 30, true},
		{"512M", totalRAM, 512 << 20, true},
		{"1024K", totalRAM, 1 << 20, true},
		{"1000000", totalRAM, 1000000, true},
		{"25%", totalRAM, 2 << 30, true}, // 25% of 8 GiB = 2 GiB
		{"100%", totalRAM, totalRAM, true},
		{"0", totalRAM, 0, false},
		{"", totalRAM, 0, false},
		{"infinity", totalRAM, 0, false},
		{"25%", 0, 0, false}, // percentage with unknown RAM => no limit
		{"garbage", totalRAM, 0, false},
	}
	for _, c := range cases {
		got, ok := parseSystemdBytes(c.in, c.total)
		if ok != c.wantOK || (ok && got != c.wantVal) {
			t.Errorf("parseSystemdBytes(%q, %d) = %d,%v want %d,%v", c.in, c.total, got, ok, c.wantVal, c.wantOK)
		}
	}
}

func TestDeriveJobLimits(t *testing.T) {
	const totalRAM = uint64(8) << 30

	// MemoryMax + TasksMax map; MemoryHigh/MemorySwapMax/CPUWeight are ignored.
	l := deriveJobLimits(config.ResourceLimits{
		MemoryHigh:    "20%",
		MemoryMax:     "25%",
		MemorySwapMax: "0",
		CPUWeight:     "200",
		TasksMax:      "512",
	}, totalRAM)
	if l.memBytes != 2<<30 {
		t.Errorf("memBytes = %d, want %d", l.memBytes, uint64(2)<<30)
	}
	if l.activeProcesses != 512 {
		t.Errorf("activeProcesses = %d, want 512", l.activeProcesses)
	}

	// Empty limits => zero (no Job Object engaged, byte-identical to bare run).
	if z := deriveJobLimits(config.ResourceLimits{}, totalRAM); !z.isZero() {
		t.Errorf("empty ResourceLimits should derive zero jobLimits, got %+v", z)
	}

	// CPUWeight-only must NOT engage a cap (no faithful Job Object analog).
	if z := deriveJobLimits(config.ResourceLimits{CPUWeight: "500"}, totalRAM); !z.isZero() {
		t.Errorf("CPUWeight-only should derive zero jobLimits, got %+v", z)
	}
}

// TestFormatJobLimits covers the supervisor-startup cap echo: a zero jobLimits must
// read UNCAPPED (the silent-footgun signal), and each engaged dimension must surface
// with the same memBytes/activeProcesses vocabulary as the per-job "job confined" line.
func TestFormatJobLimits(t *testing.T) {
	cases := []struct {
		l    jobLimits
		want string
	}{
		{jobLimits{}, "UNCAPPED (no resource limits applied)"},
		{jobLimits{memBytes: 2 << 30}, "memBytes=2147483648"},
		{jobLimits{activeProcesses: 200}, "activeProcesses=200"},
		{jobLimits{memBytes: 2 << 30, activeProcesses: 200}, "memBytes=2147483648 activeProcesses=200"},
	}
	for _, c := range cases {
		if got := formatJobLimits(c.l); got != c.want {
			t.Errorf("formatJobLimits(%+v) = %q, want %q", c.l, got, c.want)
		}
	}
}
