package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTree creates dir/<sub>/marker containing content, for each payload dir.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// TestAgentSnapshotRollback exercises the snapshot -> (failed swap) -> restore cycle:
// the runner must end with its ORIGINAL bin/externals, version-agnostically and
// without consulting any cache. This is the core of the upgrade rollback guarantee.
func TestAgentSnapshotRollback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bin", "Runner.Listener"), "OLD-bin")
	writeFile(t, filepath.Join(dir, "externals", "node", "node"), "OLD-ext")
	writeFile(t, filepath.Join(dir, ".runner"), "registration") // runtime state, must survive

	if err := snapshotAgentPayload(dir); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// Live payload dirs are gone; snapshots hold the originals.
	if fileExists(filepath.Join(dir, "bin")) || fileExists(filepath.Join(dir, "externals")) {
		t.Fatal("payload dirs still present after snapshot")
	}
	if readFile(t, filepath.Join(dir, "bin.srm-prev", "Runner.Listener")) != "OLD-bin" {
		t.Fatal("snapshot did not preserve bin")
	}

	// Simulate a partial extract of the NEW (broken) payload.
	writeFile(t, filepath.Join(dir, "bin", "Runner.Listener"), "NEW-bin")
	writeFile(t, filepath.Join(dir, "externals", "node", "node"), "NEW-ext")

	// Swap "fails" -> restore.
	if err := restoreAgentPayload(dir); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "bin", "Runner.Listener")); got != "OLD-bin" {
		t.Errorf("after restore bin = %q, want OLD-bin", got)
	}
	if got := readFile(t, filepath.Join(dir, "externals", "node", "node")); got != "OLD-ext" {
		t.Errorf("after restore externals = %q, want OLD-ext", got)
	}
	if got := readFile(t, filepath.Join(dir, ".runner")); got != "registration" {
		t.Errorf("runtime state lost: .runner = %q", got)
	}
	// Snapshots consumed by the restore.
	if fileExists(filepath.Join(dir, "bin.srm-prev")) || fileExists(filepath.Join(dir, "externals.srm-prev")) {
		t.Error("snapshot dirs remain after restore")
	}
}

// TestAgentSnapshotCommit verifies a successful swap discards the snapshot, leaving
// only the new payload.
func TestAgentSnapshotCommit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bin", "x"), "OLD")
	writeFile(t, filepath.Join(dir, "externals", "y"), "OLD")

	if err := snapshotAgentPayload(dir); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "bin", "x"), "NEW")
	writeFile(t, filepath.Join(dir, "externals", "y"), "NEW")
	discardAgentSnapshot(dir)

	if fileExists(filepath.Join(dir, "bin.srm-prev")) || fileExists(filepath.Join(dir, "externals.srm-prev")) {
		t.Error("snapshot not discarded after commit")
	}
	if got := readFile(t, filepath.Join(dir, "bin", "x")); got != "NEW" {
		t.Errorf("bin = %q, want NEW", got)
	}
}

// TestRecoverAgentSnapshot covers the crash-recovery preamble: a snapshot left by an
// interrupted upgrade is restored when the live payload is missing, and discarded as
// stale when the live payload is present.
func TestRecoverAgentSnapshot(t *testing.T) {
	// Case A: interrupted AFTER snapshot, BEFORE extract -> live missing, bak present.
	a := t.TempDir()
	writeFile(t, filepath.Join(a, "bin.srm-prev", "x"), "OLD")
	writeFile(t, filepath.Join(a, "externals.srm-prev", "y"), "OLD")
	recoverAgentSnapshot(a)
	if got := readFile(t, filepath.Join(a, "bin", "x")); got != "OLD" {
		t.Errorf("A: bin not restored, got %q", got)
	}
	if fileExists(filepath.Join(a, "bin.srm-prev")) {
		t.Error("A: stale snapshot remains")
	}

	// Case B: interrupted AFTER a completed swap left a stale bak -> live present.
	b := t.TempDir()
	writeFile(t, filepath.Join(b, "bin", "x"), "NEW")
	writeFile(t, filepath.Join(b, "bin.srm-prev", "x"), "OLD")
	recoverAgentSnapshot(b)
	if got := readFile(t, filepath.Join(b, "bin", "x")); got != "NEW" {
		t.Errorf("B: live payload disturbed, got %q", got)
	}
	if fileExists(filepath.Join(b, "bin.srm-prev")) {
		t.Error("B: stale snapshot not discarded")
	}
}
