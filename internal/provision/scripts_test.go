package provision

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveSetupScript locks the P4 dual meaning of a SetupScripts entry: an
// existing file is used as-is (no-op cleanup); an inline body is materialized to a
// runnable temp file the caller cleans up; a mistyped single-line path still errors.
func TestResolveSetupScript(t *testing.T) {
	// An existing file path is used verbatim; cleanup must NOT remove it.
	dir := t.TempDir()
	real := filepath.Join(dir, "setup.sh")
	if err := os.WriteFile(real, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, cleanup, err := resolveSetupScript(real, ".sh")
	if err != nil {
		t.Fatalf("existing path: %v", err)
	}
	if path != real {
		t.Errorf("existing path should be used as-is, got %q", path)
	}
	cleanup()
	if _, err := os.Stat(real); err != nil {
		t.Errorf("no-op cleanup must not remove a real file: %v", err)
	}

	// An inline body (shebang) is materialized verbatim to a temp file, then removed.
	body := "#!/usr/bin/env bash\necho inline\n"
	p, cl, err := resolveSetupScript(body, ".sh")
	if err != nil {
		t.Fatalf("inline shebang: %v", err)
	}
	if p == body {
		t.Fatal("inline body must be materialized to a path, not returned verbatim")
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("materialized file unreadable: %v", err)
	}
	if string(got) != body {
		t.Errorf("materialized content = %q, want %q", got, body)
	}
	cl()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("cleanup must remove the temp file, stat err = %v", err)
	}

	// A multi-line body with no shebang is still inline (materialized, no error).
	p2, cl2, err := resolveSetupScript("echo one\necho two\n", ".sh")
	if err != nil {
		t.Fatalf("inline multiline: %v", err)
	}
	cl2()
	if _, err := os.Stat(p2); !os.IsNotExist(err) {
		t.Errorf("cleanup must remove the multiline temp file, stat err = %v", err)
	}

	// A non-existent, single-line, no-shebang entry is a mistyped path: it errors
	// (preserving the historical behavior), not silently materialized.
	if _, _, err := resolveSetupScript("/no/such/script.sh", ".sh"); err == nil {
		t.Fatal("a missing single-line path must error, not be materialized")
	}
}

// TestScriptLabel keeps a whole inline body out of error/log lines.
func TestScriptLabel(t *testing.T) {
	if got := scriptLabel("/opt/setup.sh"); got != "/opt/setup.sh" {
		t.Errorf("path label = %q, want the path", got)
	}
	if got := scriptLabel("#!/bin/sh\necho hi"); got != "(inline script)" {
		t.Errorf("inline label = %q, want (inline script)", got)
	}
}
