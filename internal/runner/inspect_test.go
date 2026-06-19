package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadOOMKill parses the oom_kill counter from a cgroup v2 memory.events file
// and falls back to -1 on a missing file or an absent oom_kill line.
func TestReadOOMKill(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "memory.events")
	if err := os.WriteFile(p, []byte("low 0\nhigh 2\nmax 0\noom 1\noom_kill 4\noom_group_kill 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readOOMKill(p); got != 4 {
		t.Errorf("readOOMKill = %d, want 4", got)
	}
	if got := readOOMKill(filepath.Join(dir, "nope")); got != -1 {
		t.Errorf("missing file = %d, want -1", got)
	}
	noKill := filepath.Join(dir, "partial")
	if err := os.WriteFile(noKill, []byte("low 0\nhigh 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readOOMKill(noKill); got != -1 {
		t.Errorf("no oom_kill line = %d, want -1", got)
	}
}

// TestReadCgroupInt reads a single-integer cgroup file, yielding -1 for "max"
// (unlimited), a missing file, or unparseable content.
func TestReadCgroupInt(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got := readCgroupInt(write("cur", "12576481280\n")); got != 12576481280 {
		t.Errorf("readCgroupInt(number) = %d, want 12576481280", got)
	}
	if got := readCgroupInt(write("max", "max\n")); got != -1 {
		t.Errorf(`readCgroupInt("max") = %d, want -1`, got)
	}
	if got := readCgroupInt(filepath.Join(dir, "missing")); got != -1 {
		t.Errorf("readCgroupInt(missing) = %d, want -1", got)
	}
}
