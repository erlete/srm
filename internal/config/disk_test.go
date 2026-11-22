package config

import "testing"

func TestParseMinFreeDisk(t *testing.T) {
	for _, tc := range []struct {
		in    string
		bytes uint64
		pct   float64
	}{
		{"", 0, 0},
		{"20G", 20 << 30, 0},
		{"20GiB", 20 << 30, 0},
		{"20gb", 20 << 30, 0},
		{"500M", 500 << 20, 0},
		{"1.5T", 3 << 39, 0},
		{"10%", 0, 10},
		{" 12.5 % ", 0, 12.5},
	} {
		b, p, err := ParseMinFreeDisk(tc.in)
		if err != nil || b != tc.bytes || p != tc.pct {
			t.Errorf("%q = %d, %v, %v; want %d, %v", tc.in, b, p, err, tc.bytes, tc.pct)
		}
	}
	for _, bad := range []string{"20", "G", "abc", "-5G", "0G", "0%", "100%", "150%", "x%"} {
		if _, _, err := ParseMinFreeDisk(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestMinFreeBytes(t *testing.T) {
	c := &Config{MinFreeDisk: "10%"}
	if got, _ := c.MinFreeBytes(200 << 30); got != 20<<30 {
		t.Errorf("10%% of 200G = %d", got)
	}
	c.MinFreeDisk = "15G"
	if got, _ := c.MinFreeBytes(200 << 30); got != 15<<30 {
		t.Errorf("15G = %d", got)
	}
	c.MinFreeDisk = "oops"
	if got, err := c.MinFreeBytes(200 << 30); got != 0 || err == nil {
		t.Errorf("invalid value: %d, %v", got, err)
	}
}
