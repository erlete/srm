package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestReclaimPath: a normal tree is removed cleanly (no forced fallback), and a missing
// path is a no-op success. The forced branch needs root + an immutable/subuid tree, so
// it is exercised on the host, not here.
func TestReclaimPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	nested := filepath.Join(dir, "_work", "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "_work")
	if err := reclaimPath(ctx, target); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("path should be gone, stat err = %v", err)
	}
	if err := reclaimPath(ctx, filepath.Join(dir, "does-not-exist")); err != nil {
		t.Fatalf("reclaim missing path should be a no-op, got %v", err)
	}
}

// TestUnmountStaleSlotMountsNoop: no mounts under a fresh dir (and no mountinfo on a
// non-Linux dev box) must be a clean no-op that never panics.
func TestUnmountStaleSlotMountsNoop(t *testing.T) {
	unmountStaleSlotMounts(context.Background(), t.TempDir())
}
