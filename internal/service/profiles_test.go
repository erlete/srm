package service

import (
	"path/filepath"
	"testing"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/secrets"
)

// TestProfilesRoundTrip: a per-org create-preset persists through the real loader,
// re-setting by name REPLACES (never duplicates), and removing an absent profile is
// a loud error. Guards the TUI profile editor + `srm config profiles` save path.
func TestProfilesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{RunnerUser: "srm", Orgs: []config.OrgConfig{{Name: "acme"}, {Name: "globex"}}}
	m := New(cfg, secrets.EnvStore{}, path)

	if len(m.Profiles("acme")) != 0 {
		t.Fatalf("fresh org should have no profiles")
	}
	p := core.RunnerProfile{Name: "gpu", Labels: []string{"gpu", "large"}, GroupID: 7, Ephemeral: true, DefaultContainerImage: "cuda:12"}
	if err := m.UpsertProfile("acme", p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Re-set by the same name must replace, not append.
	p2 := p
	p2.Labels = []string{"gpu"}
	if err := m.UpsertProfile("acme", p2); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if got := m.Profiles("acme"); len(got) != 1 || len(got[0].Labels) != 1 {
		t.Fatalf("re-set should replace in place, got %+v", got)
	}
	// It must NOT bleed into the other org.
	if len(m.Profiles("globex")) != 0 {
		t.Fatalf("profile leaked into globex")
	}

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	oc, ok := got.Org("acme")
	if !ok || len(oc.Profiles) != 1 || oc.Profiles[0].Name != "gpu" || !oc.Profiles[0].Ephemeral || oc.Profiles[0].GroupID != 7 {
		t.Fatalf("persisted profile wrong: %+v", oc)
	}

	if err := m.RemoveProfile("acme", "gpu"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(m.Profiles("acme")) != 0 {
		t.Fatalf("profile should be gone after remove")
	}
	if err := m.RemoveProfile("acme", "gpu"); err == nil {
		t.Fatal("removing an absent profile should error")
	}
	if err := m.UpsertProfile("nope", p); err == nil {
		t.Fatal("upsert to an unknown org should error")
	}
}

// TestApplyProfileDefaults: a named profile seeds empty create fields, explicit
// values win, and a nature mismatch (persistent create vs ephemeral profile) is a
// hard error rather than a silent switch.
func TestApplyProfileDefaults(t *testing.T) {
	cfg := &config.Config{RunnerUser: "srm", Orgs: []config.OrgConfig{{Name: "acme", Profiles: []core.RunnerProfile{
		{Name: "eph", Labels: []string{"gpu"}, GroupID: 9, Ephemeral: true},
		{Name: "persist", Labels: []string{"big"}, GroupID: 3, Ephemeral: false},
	}}}}
	m := New(cfg, secrets.EnvStore{}, filepath.Join(t.TempDir(), "config.yaml"))

	// Empty spec seeded from the ephemeral profile.
	spec := DeploySpec{Org: "acme", Profile: "eph"}
	if err := m.applyProfileDefaults("acme", &spec, true); err != nil {
		t.Fatalf("apply eph: %v", err)
	}
	if len(spec.Labels) != 1 || spec.Labels[0] != "gpu" || spec.GroupID != 9 {
		t.Fatalf("profile did not seed spec: %+v", spec)
	}

	// Explicit labels win; group id already set is not overwritten.
	spec2 := DeploySpec{Org: "acme", Profile: "eph", Labels: []string{"custom"}, GroupID: 1}
	if err := m.applyProfileDefaults("acme", &spec2, true); err != nil {
		t.Fatalf("apply eph 2: %v", err)
	}
	if len(spec2.Labels) != 1 || spec2.Labels[0] != "custom" || spec2.GroupID != 1 {
		t.Fatalf("explicit values should win: %+v", spec2)
	}

	// Nature mismatch: the persistent profile used on an ephemeral create.
	if err := m.applyProfileDefaults("acme", &DeploySpec{Org: "acme", Profile: "persist"}, true); err == nil {
		t.Fatal("nature mismatch should error")
	}
	// Unknown profile.
	if err := m.applyProfileDefaults("acme", &DeploySpec{Org: "acme", Profile: "ghost"}, true); err == nil {
		t.Fatal("unknown profile should error")
	}
	// No profile: no-op, no error.
	if err := m.applyProfileDefaults("acme", &DeploySpec{Org: "acme"}, false); err != nil {
		t.Fatalf("empty profile should be a no-op: %v", err)
	}
}
