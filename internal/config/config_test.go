package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/erlete/srm/internal/core"
)

// TestConfigRoundTrip ensures the yaml tags (used by `srm init` to write) and
// the koanf tags (used by Load to read) agree, so a written config loads back
// identically.
func TestConfigRoundTrip(t *testing.T) {
	in := &Config{
		Concurrency:   8,
		RunnerVersion: "2.335.1",
		Orgs: []OrgConfig{{
			Name:           "acme",
			AppID:          123456,
			InstallationID: 7654321,
			PrivateKeyPath: "/etc/srm/acme.pem",
			DefaultGroupID: 1,
			DefaultLabels:  []string{"self-hosted", "linux", "x64"},
			InstallRoot:    "/opt/actions-runners",
			Profiles: []core.RunnerProfile{{
				Name: "build", Labels: []string{"build"}, GroupID: 1, Ephemeral: true,
			}},
		}},
	}

	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Orgs) != 1 {
		t.Fatalf("orgs = %d, want 1", len(got.Orgs))
	}
	o := got.Orgs[0]
	if o.AppID != 123456 || o.InstallationID != 7654321 {
		t.Fatalf("ids round-tripped wrong: appID=%d installationID=%d", o.AppID, o.InstallationID)
	}
	if o.DefaultGroupID != 1 || len(o.DefaultLabels) != 3 {
		t.Fatalf("defaults round-tripped wrong: %+v", o)
	}
	if len(o.Profiles) != 1 || !o.Profiles[0].Ephemeral {
		t.Fatalf("profiles round-tripped wrong: %+v", o.Profiles)
	}
}

// TestResourcesFor checks the host-default + per-org field-level merge: a non-empty
// org field wins; unset org fields inherit the host default; unknown orgs get the
// host default; and an all-empty config yields IsZero (limits opt-in).
func TestResourcesFor(t *testing.T) {
	cfg := &Config{
		Resources: ResourceLimits{MemoryHigh: "2G", MemoryMax: "3G", MemorySwapMax: "0", CPUWeight: "100"},
		Orgs: []OrgConfig{
			{Name: "heavy", Resources: ResourceLimits{MemoryMax: "8G"}}, // override one field
			{Name: "default"}, // no override
		},
	}

	heavy := cfg.ResourcesFor("heavy")
	if heavy.MemoryMax != "8G" {
		t.Errorf("org override lost: MemoryMax = %q, want 8G", heavy.MemoryMax)
	}
	if heavy.MemoryHigh != "2G" || heavy.MemorySwapMax != "0" || heavy.CPUWeight != "100" {
		t.Errorf("unset org fields should inherit host default: %+v", heavy)
	}

	def := cfg.ResourcesFor("default")
	if def != cfg.Resources {
		t.Errorf("org with no override should equal host default: %+v", def)
	}

	unknown := cfg.ResourcesFor("nope")
	if unknown != cfg.Resources {
		t.Errorf("unknown org should yield host default: %+v", unknown)
	}

	if (&Config{}).ResourcesFor("x").IsZero() != true {
		t.Error("empty config should yield zero (no) limits")
	}
}

// TestLoadResourcesYAML verifies a hand-written resources block loads via koanf —
// in particular that quoted numeric directives ("0", "4096") land in the string
// fields (the documented format), and per-org overrides parse.
func TestLoadResourcesYAML(t *testing.T) {
	yml := `
resources:
  memoryHigh: 2G
  memoryMax: 3G
  memorySwapMax: "0"
  cpuWeight: "100"
  tasksMax: "4096"
orgs:
  - name: heavy
    resources:
      memoryMax: 8G
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Resources.MemorySwapMax != "0" || got.Resources.TasksMax != "4096" || got.Resources.MemoryMax != "3G" {
		t.Fatalf("host resources mis-parsed: %+v", got.Resources)
	}
	if eff := got.ResourcesFor("heavy"); eff.MemoryMax != "8G" || eff.MemorySwapMax != "0" {
		t.Fatalf("org override mis-parsed: %+v", eff)
	}
}

// TestResourcesForAuto verifies "auto" mode: the machine-relative percentage
// defaults fill unset fields, an explicit host field overrides one auto value,
// a per-org field overrides host+auto, and auto never yields IsZero (the fleet is
// always bounded). Off mode (ResourceMode == "") is unaffected — see TestResourcesFor.
func TestResourcesForAuto(t *testing.T) {
	cfg := &Config{
		ResourceMode: ResourceModeAuto,
		Resources:    ResourceLimits{MemoryMax: "30%"}, // host overrides one auto field
		Orgs: []OrgConfig{
			{Name: "heavy", Resources: ResourceLimits{MemoryMax: "50%"}},
			{Name: "plain"},
		},
	}

	plain := cfg.ResourcesFor("plain")
	if plain.MemoryHigh != "20%" {
		t.Errorf("auto MemoryHigh = %q, want 20%%", plain.MemoryHigh)
	}
	if plain.MemorySwapMax != "0" {
		t.Errorf("auto MemorySwapMax = %q, want 0", plain.MemorySwapMax)
	}
	if plain.MemoryMax != "30%" {
		t.Errorf("host override MemoryMax = %q, want 30%% (host wins over auto)", plain.MemoryMax)
	}
	if plain.IsZero() {
		t.Error("auto mode must always be bounded (never IsZero)")
	}

	heavy := cfg.ResourcesFor("heavy")
	if heavy.MemoryMax != "50%" {
		t.Errorf("per-org override MemoryMax = %q, want 50%%", heavy.MemoryMax)
	}
	if heavy.MemoryHigh != "20%" || heavy.MemorySwapMax != "0" {
		t.Errorf("per-org unset fields should inherit auto+host: %+v", heavy)
	}

	// Sanity: with auto OFF the same explicit-only config behaves as before.
	off := &Config{Resources: ResourceLimits{MemoryMax: "30%"}}
	if off.ResourcesFor("plain").MemoryHigh != "" {
		t.Error("off mode must not synthesize auto fields")
	}
}

// TestSliceMemoryMaxOrDefault verifies the aggregate slice ceiling falls back to
// the pre-packaged default when unset and honors an explicit override otherwise.
func TestSliceMemoryMaxOrDefault(t *testing.T) {
	if got := (&Config{}).SliceMemoryMaxOrDefault(); got != AutoSliceMemoryMax {
		t.Errorf("unset slice cap = %q, want default %q", got, AutoSliceMemoryMax)
	}
	if got := (&Config{SliceMemoryMax: "80%"}).SliceMemoryMaxOrDefault(); got != "80%" {
		t.Errorf("override slice cap = %q, want 80%%", got)
	}
}

// TestLoadResourceModeAuto verifies `resourceMode: auto` parses and resolves to
// the machine-relative defaults for an org with no overrides.
func TestLoadResourceModeAuto(t *testing.T) {
	yml := "resourceMode: auto\norgs:\n  - name: solo\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.ResourceMode != ResourceModeAuto {
		t.Fatalf("ResourceMode = %q, want %q", got.ResourceMode, ResourceModeAuto)
	}
	if eff := got.ResourcesFor("solo"); eff.MemoryMax != "25%" || eff.MemorySwapMax != "0" {
		t.Fatalf("auto resolution wrong: %+v", eff)
	}
}

// TestResourceModeValidation ensures a typo'd/case-variant resourceMode is
// rejected at load — a silent fallthrough would leave the fleet UNBOUNDED while
// the operator believes it is capped.
func TestResourceModeValidation(t *testing.T) {
	load := func(yml string) error {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		return err
	}
	if err := load("resourceMode: auto\norgs:\n  - name: a\n"); err != nil {
		t.Errorf("auto must load: %v", err)
	}
	if err := load("orgs:\n  - name: a\n"); err != nil {
		t.Errorf("omitted resourceMode must load: %v", err)
	}
	if err := load("resourceMode: Auto\norgs:\n  - name: a\n"); err == nil {
		t.Error("case-variant 'Auto' must be rejected")
	}
	if err := load("resourceMode: bogus\norgs:\n  - name: a\n"); err == nil {
		t.Error("invalid resourceMode must be rejected")
	}
}

// TestHardeningConfig verifies the opt-in persistent-hardening toggle parses from
// yaml, defaults off, and is omitted from a fresh marshaled config so `srm init`
// output stays unchanged.
func TestHardeningConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("hardening:\n  protectProc: true\norgs:\n  - name: a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !got.Hardening.ProtectProc {
		t.Error("hardening.protectProc: true did not load")
	}
	if (&Config{}).Hardening.ProtectProc {
		t.Error("Hardening.ProtectProc must default to false")
	}
	data, err := yaml.Marshal(&Config{RunnerUser: "srm", Orgs: []OrgConfig{{Name: "acme"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hardening:") {
		t.Errorf("zero hardening block leaked into marshaled config:\n%s", data)
	}
}

// TestSlug pins username normalization for the live org names — these feed the
// per-org service username and must be valid lowercase Linux user names.
func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Acme":        "acme",
		"Globex": "globex",
		"Acme_Co":         "acme-co",
		"-weird.Name-":    "weird-name",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestResolutionDefaultMode verifies that with isolation OFF (the default), the
// per-org resolvers return the shared user verbatim and EMPTY cache roots, so the
// orchestrator falls back to the historical shared paths — i.e. nothing changes.
func TestResolutionDefaultMode(t *testing.T) {
	cfg := &Config{RunnerUser: "srm", Orgs: []OrgConfig{{Name: "Acme"}, {Name: "Globex"}}}
	for _, org := range []string{"Acme", "Globex"} {
		if u := cfg.RunnerUserFor(org); u != "srm" {
			t.Errorf("default RunnerUserFor(%q) = %q, want srm", org, u)
		}
		if r := cfg.CacheRootFor(org); r != "" {
			t.Errorf("default CacheRootFor(%q) = %q, want empty (shared)", org, r)
		}
		if r := cfg.ToolCacheFor(org); r != "" {
			t.Errorf("default ToolCacheFor(%q) = %q, want empty (shared)", org, r)
		}
	}
}

// TestResolutionIsolatedMode verifies per-org users + per-org cache paths, and
// that an explicit OrgConfig.RunnerUser override wins.
func TestResolutionIsolatedMode(t *testing.T) {
	cfg := &Config{
		RunnerUser: "srm",
		Isolation:  Isolation{PerOrgUsers: true},
		Orgs:       []OrgConfig{{Name: "Acme"}, {Name: "Globex", RunnerUser: "cd-runner"}},
	}
	if u := cfg.RunnerUserFor("Acme"); u != "srm-acme" {
		t.Errorf("RunnerUserFor(Acme) = %q, want srm-acme", u)
	}
	if u := cfg.RunnerUserFor("Globex"); u != "cd-runner" {
		t.Errorf("org RunnerUser override lost: %q", u)
	}
	if r := cfg.CacheRootFor("Acme"); r != "/opt/srm-cache/Acme" {
		t.Errorf("CacheRootFor(Acme) = %q", r)
	}
	if r := cfg.ToolCacheFor("Acme"); r != "/opt/hostedtoolcache/Acme" {
		t.Errorf("ToolCacheFor(Acme) = %q", r)
	}
	// Empty org (host-wide callers) must not produce "/opt/srm-cache/".
	if r := cfg.CacheRootFor(""); r != "" {
		t.Errorf("CacheRootFor(\"\") = %q, want empty", r)
	}
}

// TestValidateIsolation guards the isolation security boundary: distinct orgs
// must resolve to distinct, valid service users (else they'd share a uid and
// isolation silently breaks). No-op when isolation is off.
func TestValidateIsolation(t *testing.T) {
	// Collision: two org names slug to the same user.
	collide := &Config{
		RunnerUser: "srm",
		Isolation:  Isolation{PerOrgUsers: true},
		Orgs:       []OrgConfig{{Name: "my-org"}, {Name: "my_org"}}, // both → srm-my-org
	}
	if err := collide.validateIsolation(); err == nil {
		t.Error("expected collision error for my-org/my_org → srm-my-org")
	}
	// Same collision but isolation OFF → allowed (validation skipped).
	collide.Isolation.PerOrgUsers = false
	if err := collide.validateIsolation(); err != nil {
		t.Errorf("collision must be ignored in single-user mode: %v", err)
	}
	// Explicit per-org override resolves the collision.
	fixed := &Config{
		RunnerUser: "srm",
		Isolation:  Isolation{PerOrgUsers: true},
		Orgs:       []OrgConfig{{Name: "my-org"}, {Name: "my_org", RunnerUser: "srm-myorg2"}},
	}
	if err := fixed.validateIsolation(); err != nil {
		t.Errorf("distinct override should validate: %v", err)
	}
	// Empty slug → "srm-" is an invalid user name.
	empty := &Config{RunnerUser: "srm", Isolation: Isolation{PerOrgUsers: true}, Orgs: []OrgConfig{{Name: "@@@"}}}
	if err := empty.validateIsolation(); err == nil {
		t.Error("expected invalid-user error for empty-slug org")
	}
	// Realistic distinct orgs validate cleanly.
	ok := &Config{RunnerUser: "srm", Isolation: Isolation{PerOrgUsers: true}, Orgs: []OrgConfig{{Name: "Acme"}, {Name: "Globex"}}}
	if err := ok.validateIsolation(); err != nil {
		t.Errorf("Acme/Globex should validate: %v", err)
	}
}

// TestIsolationOmittedFromInit verifies a fresh config marshals WITHOUT an
// isolation block (the new opt-in field) and without a per-org runnerUser line,
// so `srm init` output is unchanged, and round-trips to PerOrgUsers=false. The
// top-level runnerUser line is pre-existing and intentionally always present.
func TestIsolationOmittedFromInit(t *testing.T) {
	in := &Config{RunnerUser: "srm", Orgs: []OrgConfig{{Name: "acme"}}}
	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if strings.Contains(s, "isolation:") {
		t.Fatalf("zero isolation block leaked into marshaled config:\n%s", s)
	}
	// The only runnerUser line allowed is the top-level one; the org must not emit
	// its own (OrgConfig.RunnerUser is omitempty).
	if strings.Count(s, "runnerUser:") != 1 {
		t.Fatalf("expected exactly one (top-level) runnerUser line:\n%s", s)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Isolation.PerOrgUsers {
		t.Error("PerOrgUsers should default false when absent")
	}
}
