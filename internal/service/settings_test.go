package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/secrets"
)

// TestApplyResourceSettingsRoundTrip is the load-bearing test for the TUI Settings
// save path: an edit must persist to disk and survive a reload through the REAL
// loader (which also re-runs validation), so a TUI-saved config is never one srm
// would then refuse to start on.
func TestApplyResourceSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{Concurrency: 8, RunnerVersion: "2.0.0", RunnerUser: "srm"}
	m := New(cfg, secrets.EnvStore{}, path)

	want := ResourceSettings{
		Mode:      config.ResourceModeAuto,
		Resources: config.ResourceLimits{MemoryMax: "30%", MemorySwapMax: "0"},
		SliceMax:  "80%",
	}
	if err := m.ApplyResourceSettings(want); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config not written: %v", err)
	}

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.ResourceMode != config.ResourceModeAuto {
		t.Errorf("mode = %q, want %q", got.ResourceMode, config.ResourceModeAuto)
	}
	if got.Resources.MemoryMax != "30%" {
		t.Errorf("memoryMax = %q, want 30%%", got.Resources.MemoryMax)
	}
	if got.SliceMemoryMax != "80%" {
		t.Errorf("sliceMemoryMax = %q, want 80%%", got.SliceMemoryMax)
	}
	if d := got.SliceMemoryMaxOrDefault(); d != "80%" {
		t.Errorf("SliceMemoryMaxOrDefault = %q, want 80%%", d)
	}
}

// TestApplyResourceSettingsRejectsBadMode guards the in-process write: a typo'd
// mode must be rejected at apply time (not silently saved to be caught only on the
// NEXT load), since a bad mode leaves jobs unbounded.
func TestApplyResourceSettingsRejectsBadMode(t *testing.T) {
	m := New(&config.Config{}, secrets.EnvStore{}, filepath.Join(t.TempDir(), "config.yaml"))
	if err := m.ApplyResourceSettings(ResourceSettings{Mode: "bogus"}); err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

// TestSaveConfigNoPath ensures the no-path Manager (used in tests/embeddings)
// fails loudly rather than writing to a surprising location.
func TestSaveConfigNoPath(t *testing.T) {
	m := New(&config.Config{}, secrets.EnvStore{}, "")
	if err := m.SaveConfig(); err == nil {
		t.Fatal("expected error when no config path is set")
	}
}
