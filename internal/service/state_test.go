package service

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStateLoadMissingIsEmpty locks the backward-compat invariant: a host with no
// manifest (every srm <= v1.3.0 host) loads cleanly as an empty, current-version
// manifest rather than erroring.
func TestStateLoadMissingIsEmpty(t *testing.T) {
	m, err := LoadState(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("LoadState(missing) error = %v, want nil", err)
	}
	if m.StateVersion != CurrentStateVersion {
		t.Errorf("StateVersion = %d, want %d", m.StateVersion, CurrentStateVersion)
	}
	if len(m.Runners) != 0 {
		t.Errorf("Runners = %v, want empty", m.Runners)
	}
}

// TestStateCorruptIsError ensures a torn/garbage manifest is surfaced, never
// silently discarded - dropping it would erase the host-bound ids and version
// history the upgrade safety gates depend on.
func TestStateCorruptIsError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(p); err == nil {
		t.Fatal("LoadState(corrupt) error = nil, want non-nil")
	}
}

// TestStateRoundTrip exercises Put/Get/Remove and an atomic save+reload.
func TestStateRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "state.json") // sub dir must be auto-created
	m, err := LoadState(p)
	if err != nil {
		t.Fatal(err)
	}

	m.Put(RunnerRecord{Kind: KindPersistent, Org: "Acme", Name: "ci-1", AgentVersion: "2.330.0", GitHubRunnerID: 42, TemplateVersion: 1})
	m.Put(RunnerRecord{Kind: KindEphemeral, Org: "Acme", Name: "1", AgentVersion: "2.330.0"})

	// A persistent "1" and an ephemeral "1" are DISTINCT keys (kind is part of it).
	m.Put(RunnerRecord{Kind: KindPersistent, Org: "Acme", Name: "1", AgentVersion: "2.331.0"})
	if len(m.Runners) != 3 {
		t.Fatalf("len(Runners) = %d, want 3 (kind disambiguates name collision)", len(m.Runners))
	}

	// Upsert replaces in place, preserving order and count.
	m.Put(RunnerRecord{Kind: KindPersistent, Org: "Acme", Name: "ci-1", AgentVersion: "2.335.1", PreviousVersion: "2.330.0", GitHubRunnerID: 42})
	if len(m.Runners) != 3 {
		t.Fatalf("after upsert len(Runners) = %d, want 3", len(m.Runners))
	}

	if err := m.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 0600 + parent auto-created.
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeIsUnix() && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}

	reloaded, err := LoadState(p)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Get(KindPersistent, "Acme", "ci-1")
	if !ok {
		t.Fatal("Get(persistent Acme/ci-1) not found after reload")
	}
	if got.AgentVersion != "2.335.1" || got.PreviousVersion != "2.330.0" || got.GitHubRunnerID != 42 {
		t.Errorf("reloaded entry = %+v, want agent 2.335.1 / prev 2.330.0 / id 42", got)
	}
	// The ephemeral "1" must be untouched by the persistent "1" upsert.
	if e, ok := reloaded.Get(KindEphemeral, "Acme", "1"); !ok || e.AgentVersion != "2.330.0" {
		t.Errorf("ephemeral Acme/1 = %+v ok=%v, want agent 2.330.0", e, ok)
	}

	// Remove is keyed and leaves the others.
	reloaded.Remove(KindPersistent, "Acme", "ci-1")
	if _, ok := reloaded.Get(KindPersistent, "Acme", "ci-1"); ok {
		t.Error("entry still present after Remove")
	}
	if len(reloaded.Runners) != 2 {
		t.Errorf("after Remove len = %d, want 2", len(reloaded.Runners))
	}
}

// runtimeIsUnix reports whether file-mode bits are meaningful (they are not on
// Windows, where the test suite also runs during development).
func runtimeIsUnix() bool { return os.PathSeparator == '/' }
