package cli

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/erlete/srm/internal/config"
)

// toOrgConfig must trim whitespace, parse the numeric fields the form already
// validated, and split the label CSV — the same conversion `srm init` and the
// first-run wizard both rely on.
func TestOrgFieldsToOrgConfig(t *testing.T) {
	f := orgFields{
		name:        "  acme  ",
		appIDStr:    " 123456 ",
		instIDStr:   "7654321",
		keyPath:     " /etc/srm/acme.pem ",
		groupIDStr:  "7",
		labelsStr:   "self-hosted, linux ,x64,",
		installRoot: " /opt/actions-runners ",
	}
	got := f.toOrgConfig()
	want := config.OrgConfig{
		Name:           "acme",
		AppID:          123456,
		InstallationID: 7654321,
		PrivateKeyPath: "/etc/srm/acme.pem",
		DefaultGroupID: 7,
		DefaultLabels:  []string{"self-hosted", "linux", "x64"},
		InstallRoot:    "/opt/actions-runners",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("toOrgConfig() = %+v, want %+v", got, want)
	}
}

func TestDefaultOrgFields(t *testing.T) {
	f := defaultOrgFields()
	if f.groupIDStr != "1" {
		t.Errorf("default groupID = %q, want \"1\"", f.groupIDStr)
	}
	if f.installRoot != config.DefaultInstallRoot {
		t.Errorf("default installRoot = %q, want %q", f.installRoot, config.DefaultInstallRoot)
	}
	if f.labelsStr == "" {
		t.Error("default labels should be non-empty")
	}
}

func TestSplitCSV(t *testing.T) {
	cases := map[string][]string{
		"a,b,c":         {"a", "b", "c"},
		" a , b ,, c ,": {"a", "b", "c"},
		"":              nil,
		"  ,  ":         nil,
	}
	for in, want := range cases {
		if got := splitCSV(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitCSV(%q) = %v, want %v", in, got, want)
		}
	}
}

// upsertOrg inserts a new org and replaces an existing one in place (no dup).
func TestUpsertOrg(t *testing.T) {
	cfg := &config.Config{}
	upsertOrg(cfg, config.OrgConfig{Name: "acme", AppID: 1})
	upsertOrg(cfg, config.OrgConfig{Name: "globex", AppID: 2})
	if len(cfg.Orgs) != 2 {
		t.Fatalf("after two inserts: %d orgs, want 2", len(cfg.Orgs))
	}
	upsertOrg(cfg, config.OrgConfig{Name: "acme", AppID: 99})
	if len(cfg.Orgs) != 2 {
		t.Fatalf("after update: %d orgs, want 2 (no dup)", len(cfg.Orgs))
	}
	if oc, ok := cfg.Org("acme"); !ok || oc.AppID != 99 {
		t.Fatalf("acme not updated in place: %+v ok=%v", oc, ok)
	}
}

// writeConfig must produce a file config.Load reads back with the org intact —
// the round-trip the wizard depends on before it reloads the manager.
func TestWriteConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	cfg := &config.Config{Orgs: []config.OrgConfig{{
		Name: "acme", AppID: 123, InstallationID: 456,
		PrivateKeyPath: "/etc/srm/acme.pem", DefaultGroupID: 1,
		DefaultLabels: []string{"self-hosted", "linux", "x64"},
		InstallRoot:   "/opt/actions-runners",
	}}}
	if err := writeConfig(path, cfg); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.OrgNames(); !reflect.DeepEqual(got, []string{"acme"}) {
		t.Fatalf("loaded orgs = %v, want [acme]", got)
	}
	oc, ok := loaded.Org("acme")
	if !ok || oc.AppID != 123 || oc.InstallationID != 456 {
		t.Fatalf("round-tripped org wrong: %+v ok=%v", oc, ok)
	}
}

// ensureConfigured must be a pure no-op when orgs already exist — it must never
// trigger the interactive wizard on a configured host (which would block on a TTY).
func TestEnsureConfiguredNoopWhenOrgsExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{Orgs: []config.OrgConfig{{Name: "acme", AppID: 1, InstallationID: 2}}}
	if err := writeConfig(path, cfg); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	orig := flagConfig
	flagConfig = path
	defer func() { flagConfig = orig }()

	if err := ensureConfigured(); err != nil {
		t.Fatalf("ensureConfigured with orgs present should be a nil no-op, got %v", err)
	}
}
