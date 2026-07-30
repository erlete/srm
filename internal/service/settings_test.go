package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
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

// TestApplyHostPolicyRoundTrip: the host-policy toggles persist and survive a reload
// through the real loader, and HostPolicy reads them back. Guards the TUI Settings +
// `srm config set` save path.
func TestApplyHostPolicyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	m := New(&config.Config{RunnerUser: "srm"}, secrets.EnvStore{}, path)

	if p := m.HostPolicy(); p.RootlessDinD || p.PerOrgUsers || p.ProtectProc || p.BuildkitImage != "" {
		t.Fatalf("fresh policy should be all-off, got %+v", p)
	}
	want := HostPolicy{RootlessDinD: true, BuildkitImage: "moby/buildkit:custom", PerOrgUsers: true, ProtectProc: true}
	if err := m.ApplyHostPolicy(want); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := m.HostPolicy(); got != want {
		t.Fatalf("in-memory policy = %+v, want %+v", got, want)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !got.Docker.RootlessDinD || got.Docker.BuildkitImage != "moby/buildkit:custom" || !got.Isolation.PerOrgUsers || !got.Hardening.ProtectProc {
		t.Fatalf("persisted policy wrong: docker=%+v isolation=%+v hardening=%+v", got.Docker, got.Isolation, got.Hardening)
	}
}

// TestApplyHostManifestRoundTrip: the host dependency manifest persists and survives
// a reload through the real loader, INCLUDING a multi-line inline setup-script body
// (P4) - which must round-trip through YAML intact for the materialize-at-provision
// path to work. Guards the TUI manifest editor + `srm config manifest` save path.
func TestApplyHostManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	m := New(&config.Config{RunnerUser: "srm"}, secrets.EnvStore{}, path)

	if !m.HostManifest().Empty() {
		t.Fatalf("fresh manifest should be empty, got %+v", m.HostManifest())
	}
	inline := "#!/usr/bin/env bash\nset -euo pipefail\necho hello\n"
	want := core.DependencyManifest{
		AptPackages:          []string{"jq", "ripgrep"},
		SetupScripts:         []string{"/opt/setup.sh", inline},
		ToolCacheSeeds:       []string{"node@22.11.0"},
		PersistentCachePaths: []string{"/var/cache/srm"},
	}
	if err := m.ApplyHostManifest(want); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got.Host.AptPackages) != 2 || got.Host.AptPackages[1] != "ripgrep" {
		t.Errorf("apt round-trip wrong: %+v", got.Host.AptPackages)
	}
	if len(got.Host.SetupScripts) != 2 {
		t.Fatalf("scripts round-trip wrong count: %+v", got.Host.SetupScripts)
	}
	if got.Host.SetupScripts[0] != "/opt/setup.sh" {
		t.Errorf("path entry mangled: %q", got.Host.SetupScripts[0])
	}
	if got.Host.SetupScripts[1] != inline || !core.IsInlineScript(got.Host.SetupScripts[1]) {
		t.Errorf("inline body must survive YAML verbatim, got %q", got.Host.SetupScripts[1])
	}
	if len(got.Host.ToolCacheSeeds) != 1 || got.Host.ToolCacheSeeds[0] != "node@22.11.0" {
		t.Errorf("seeds round-trip wrong: %+v", got.Host.ToolCacheSeeds)
	}
	if len(got.Host.PersistentCachePaths) != 1 {
		t.Errorf("cache paths round-trip wrong: %+v", got.Host.PersistentCachePaths)
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
