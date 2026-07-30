package service

import (
	"reflect"
	"testing"
)

// TestAllocateEphemeralSlots locks the additive next-free-slot behavior that stops
// a repeat "create N" from clobbering existing lanes (the 4+4=4 bug): allocation
// picks the lowest free ids starting at 1, reusing a gap before extending.
func TestAllocateEphemeralSlots(t *testing.T) {
	set := func(ids ...int) map[int]bool {
		m := map[int]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	cases := []struct {
		name     string
		occupied map[int]bool
		count    int
		want     []string
	}{
		{"empty host -> 1..N", set(), 4, []string{"1", "2", "3", "4"}},
		{"add 4 when 1-4 exist -> 5..8 (the 4+4 fix)", set(1, 2, 3, 4), 4, []string{"5", "6", "7", "8"}},
		{"reuse a freed gap before extending", set(1, 2, 4), 2, []string{"3", "5"}},
		{"count zero -> none", set(1), 0, []string{}},
		{"non-contiguous occupancy", set(2, 5), 3, []string{"1", "3", "4"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := allocateEphemeralSlots(tc.occupied, tc.count)
			if len(tc.want) == 0 && len(got) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("allocateEphemeralSlots(%v, %d) = %v, want %v", tc.occupied, tc.count, got, tc.want)
			}
		})
	}
}
