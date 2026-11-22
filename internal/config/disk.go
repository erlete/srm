package config

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseMinFreeDisk parses a minFreeDisk value: a size with a binary unit
// ("500M", "20G", "1.5T"; K/M/G/T, optional "i"/"B" as in "20GiB") or a
// percentage of the filesystem ("10%"). Exactly one of bytes and percent is
// non-zero; both are zero for an empty value (off).
func ParseMinFreeDisk(s string) (bytes uint64, percent float64, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, nil
	}
	if p, ok := strings.CutSuffix(s, "%"); ok {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v <= 0 || v >= 100 {
			return 0, 0, fmt.Errorf("minFreeDisk %q: a percentage must be between 0 and 100", s)
		}
		return 0, v, nil
	}
	num := strings.TrimRight(strings.ToUpper(s), "IB")
	mult := uint64(1)
	if n := len(num); n > 0 {
		switch num[n-1] {
		case 'K':
			mult = 1 << 10
		case 'M':
			mult = 1 << 20
		case 'G':
			mult = 1 << 30
		case 'T':
			mult = 1 << 40
		}
		if mult > 1 {
			num = num[:n-1]
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil || v <= 0 || mult == 1 {
		return 0, 0, fmt.Errorf("minFreeDisk %q: want a size with a unit (e.g. 20G, 500M) or a percentage (e.g. 10%%)", s)
	}
	return uint64(v * float64(mult)), 0, nil
}

// MinFreeBytes resolves MinFreeDisk against a filesystem of total bytes. 0 means
// no minimum (unset); an invalid value returns the parse error, and callers treat
// it as unset (a typo must not stop every slot).
func (c *Config) MinFreeBytes(total uint64) (uint64, error) {
	bytes, pct, err := ParseMinFreeDisk(c.MinFreeDisk)
	if err != nil {
		return 0, err
	}
	if pct > 0 {
		return uint64(float64(total) * pct / 100), nil
	}
	return bytes, nil
}
