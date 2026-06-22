package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWindowsSvcName locks the Windows Service name contract: it MUST equal the name
// config.cmd --runasservice assigns ("actions.runner.<org>.<name>", no ".service"
// suffix), or SCM start/stop/delete and ListUnits would target the wrong name.
func TestWindowsSvcName(t *testing.T) {
	w := &windows{installRoot: `C:\actions-runners`}
	got := w.svcName("DLT-Code", "srm-ci-win-1")
	want := "actions.runner.DLT-Code.srm-ci-win-1"
	if got != want {
		t.Errorf("svcName = %q, want %q", got, want)
	}
	// No ".service" systemd suffix on the SCM name.
	if got[len(got)-len(".service"):] == ".service" {
		t.Error("svcName must not carry the systemd .service suffix")
	}
}

// TestPSQuote ensures single quotes are doubled for safe embedding in a
// single-quoted PowerShell string (so a crafted org/runner name can't break out of
// the quoted argument).
func TestPSQuote(t *testing.T) {
	cases := map[string]string{
		"plain":        "plain",
		"a'b":          "a''b",
		"'":            "''",
		"x''y":         "x''''y",
		"actions.run*": "actions.run*",
	}
	for in, want := range cases {
		if got := psQuote(in); got != want {
			t.Errorf("psQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestICaclsGrantee locks the grantee resolution: the built-in passwordless service
// accounts MUST go to icacls as their well-known SID (the "*S-1-..." form) in any
// common spelling/casing, because the localized display names fail to resolve (the
// error 1332 that broke `runners create`); a real account name passes through
// verbatim for icacls to resolve itself.
func TestICaclsGrantee(t *testing.T) {
	cases := map[string]string{
		`NT AUTHORITY\NETWORK SERVICE`: `*S-1-5-20`,
		`network service`:             `*S-1-5-20`,
		`NT AUTHORITY\LOCAL SERVICE`:  `*S-1-5-19`,
		`Local Service`:               `*S-1-5-19`,
		`SYSTEM`:                      `*S-1-5-18`,
		`LocalSystem`:                 `*S-1-5-18`,
		`acme\\ci-runner`:             `acme\\ci-runner`, // real account -> verbatim
		`srm`:                         `srm`,             // not a built-in -> verbatim
	}
	for in, want := range cases {
		if got := icaclsGrantee(in); got != want {
			t.Errorf("icaclsGrantee(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRemoveRunnerTree confirms the resilient removal deletes a populated tree,
// including a read-only file (which Windows would otherwise refuse to delete), and
// is a no-op on an already-absent directory.
func TestRemoveRunnerTree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "DLT-Code", "ci-1")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	ro := filepath.Join(dir, "bin", "clrjit.dll")
	if err := os.WriteFile(ro, []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o444); err != nil { // ensure read-only attribute
		t.Fatal(err)
	}
	if err := removeRunnerTree(dir); err != nil {
		t.Fatalf("removeRunnerTree: %v", err)
	}
	if fileExists(dir) {
		t.Error("dir still exists after removeRunnerTree")
	}
	// Idempotent: removing an absent tree is not an error.
	if err := removeRunnerTree(dir); err != nil {
		t.Errorf("removeRunnerTree on absent dir: %v", err)
	}
}

// TestWindowsRunnerDir confirms the org-namespaced install path matches the layout
// the rest of the code (RunnerIsLocal, reconcile) expects.
func TestWindowsRunnerDir(t *testing.T) {
	w := &windows{installRoot: `C:\actions-runners`}
	got := w.runnerDir("DLT-Code", "ci-1")
	want := `C:\actions-runners\DLT-Code\ci-1`
	if got != want {
		t.Errorf("runnerDir = %q, want %q", got, want)
	}
}
