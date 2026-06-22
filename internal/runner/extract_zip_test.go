package runner

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// makeZip writes a zip at path containing the given name->content entries (a name
// ending in "/" is a directory entry).
func makeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range entries {
		if name[len(name)-1] == '/' {
			if _, err := zw.Create(name); err != nil {
				t.Fatal(err)
			}
			continue
		}
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestExtractZip unpacks a runner-shaped archive (top-level scripts + nested bin/)
// and confirms contents and nesting survive.
func TestExtractZip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "runner.zip")
	makeZip(t, src, map[string]string{
		"config.cmd":          "@echo config",
		"run.cmd":             "@echo run",
		"bin/":                "",
		"bin/Runner.Listener": "binary",
		"externals/node/node": "node",
	})
	dst := filepath.Join(dir, "out")
	if err := extractZip(src, dst); err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	for name, want := range map[string]string{
		"config.cmd":          "@echo config",
		"run.cmd":             "@echo run",
		"bin/Runner.Listener": "binary",
		"externals/node/node": "node",
	} {
		b, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("missing %s: %v", name, err)
			continue
		}
		if string(b) != want {
			t.Errorf("%s = %q, want %q", name, b, want)
		}
	}
}

// TestExtractRealRunnerZip is an integration test against the actual
// actions-runner-win-x64 archive. It is skipped unless SRM_WIN_RUNNER_ZIP points to
// a downloaded copy, so CI/offline runs don't fetch ~150 MB; run it locally to prove
// the real archive (its directory ordering, large binaries, nested externals)
// extracts cleanly and yields the launchable entrypoints.
func TestExtractRealRunnerZip(t *testing.T) {
	zipPath := os.Getenv("SRM_WIN_RUNNER_ZIP")
	if zipPath == "" {
		t.Skip("set SRM_WIN_RUNNER_ZIP to a downloaded actions-runner-win-x64 zip to run")
	}
	dst := t.TempDir()
	if err := extractZip(zipPath, dst); err != nil {
		t.Fatalf("extract real runner zip: %v", err)
	}
	for _, must := range []string{"config.cmd", "run.cmd", filepath.Join("bin", "Runner.Listener.exe")} {
		if _, err := os.Stat(filepath.Join(dst, must)); err != nil {
			t.Errorf("expected %s in extracted runner: %v", must, err)
		}
	}
}

// TestExtractZipRejectsTraversal locks the Zip-Slip guard: an entry escaping dst
// must be refused, not written outside the target tree.
func TestExtractZipRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "evil.zip")
	makeZip(t, src, map[string]string{`../escape.txt`: "pwned"})
	dst := filepath.Join(dir, "out")
	if err := extractZip(src, dst); err == nil {
		t.Fatal("extractZip accepted a path-traversal entry, want rejection")
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Fatal("traversal entry escaped the destination tree")
	}
}
