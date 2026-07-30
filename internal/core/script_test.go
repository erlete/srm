package core

import "testing"

// TestIsInlineScript locks the predicate that decides whether a SetupScripts entry
// is an inline body (materialize at provision) or a path (run in place). A shebang or
// an embedded newline is inline; a bare single-line string is a path.
func TestIsInlineScript(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"/opt/setup.sh", false},
		{"setup.sh", false},
		{"  /opt/with-space.sh  ", false},
		{"#!/usr/bin/env bash\necho hi", true},
		{"#!/bin/sh", true},          // shebang alone (single line)
		{"echo one\necho two", true}, // multi-line, no shebang
		{"", false},
	}
	for _, c := range cases {
		if got := IsInlineScript(c.in); got != c.want {
			t.Errorf("IsInlineScript(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
