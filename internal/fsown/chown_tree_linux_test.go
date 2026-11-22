//go:build linux

package fsown

import (
	"os"
	"path/filepath"
	"testing"
)

// With the real stat: a tree the target user already owns needs no lchown at all
// (the normal case on every create/refresh). The target is this process's own
// uid:gid, which owns everything it just created.
func TestChownTreeOwnedTreeWritesNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "b", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldL, oldU := lchownFn, lookupIDsFn
	t.Cleanup(func() { lchownFn, lookupIDsFn = oldL, oldU })
	lookupIDsFn = func(string) (int, int, error) { return os.Getuid(), os.Getgid(), nil }
	var n int
	lchownFn = func(string, int, int) error { n++; return nil }
	if err := ChownTree(root, "me", nil); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d lchown calls on an already-owned tree, want 0", n)
	}
}
