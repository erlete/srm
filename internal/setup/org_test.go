package setup

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"reflect"
	"testing"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
)

// ToOrgConfig must trim whitespace, parse the numeric fields the form already
// validated, and split the label CSV - the same conversion `srm init`, the first-run
// wizard, and the TUI onboard wizard all rely on.
func TestOrgFieldsToOrgConfig(t *testing.T) {
	f := OrgFields{
		Name:        "  acme  ",
		AppIDStr:    " 123456 ",
		InstIDStr:   "7654321",
		KeyPath:     " /etc/srm/acme.pem ",
		GroupIDStr:  "7",
		LabelsStr:   "self-hosted, linux ,x64,",
		InstallRoot: " /opt/actions-runners ",
	}
	got := f.ToOrgConfig()
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
		t.Fatalf("ToOrgConfig() = %+v, want %+v", got, want)
	}
}

func TestDefaultOrgFields(t *testing.T) {
	f := DefaultOrgFields()
	if f.GroupIDStr != "1" {
		t.Errorf("default groupID = %q, want \"1\"", f.GroupIDStr)
	}
	if f.InstallRoot != config.DefaultInstallRoot {
		t.Errorf("default installRoot = %q, want %q", f.InstallRoot, config.DefaultInstallRoot)
	}
	if f.LabelsStr == "" {
		t.Error("default labels should be non-empty")
	}
}

// ToOrgConfig in paste mode must leave PrivateKeyPath empty (the key is stored in the
// secrets store, not referenced on disk) even if a stale KeyPath is present.
func TestToOrgConfigPasteMode(t *testing.T) {
	f := OrgFields{Name: "acme", AppIDStr: "1", InstIDStr: "2", GroupIDStr: "1", KeyMode: KeyModePaste, KeyPath: "/should/be/ignored"}
	if oc := f.ToOrgConfig(); oc.PrivateKeyPath != "" {
		t.Fatalf("paste mode must blank PrivateKeyPath, got %q", oc.PrivateKeyPath)
	}
}

// ValidatePEM accepts a real RSA key in PKCS#1 and PKCS#8 PEM and rejects junk,
// without leaking key material into the error.
func TestValidatePEM(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := ValidatePEM(string(pkcs1)); err != nil {
		t.Fatalf("valid PKCS#1 key rejected: %v", err)
	}
	p8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8})
	if err := ValidatePEM(string(pkcs8)); err != nil {
		t.Fatalf("valid PKCS#8 key rejected: %v", err)
	}
	for _, bad := range []string{
		"",
		"not a key at all",
		"-----BEGIN RSA PRIVATE KEY-----\nZ2FyYmFnZQ==\n-----END RSA PRIVATE KEY-----",
	} {
		if err := ValidatePEM(bad); err == nil {
			t.Fatalf("ValidatePEM(%q) = nil, want an error", bad)
		}
	}
}

// FieldsFromOrgConfig seeds the edit form (key defaults to keep) and round-trips an
// untouched key; switching the key mode changes only the resolved PrivateKeyPath.
func TestFieldsFromOrgConfigRoundTrip(t *testing.T) {
	oc := config.OrgConfig{
		Name: "acme", AppID: 123, InstallationID: 456,
		PrivateKeyPath: "/etc/srm/acme.pem", DefaultGroupID: 7,
		DefaultLabels: []string{"self-hosted", "linux", "x64"}, InstallRoot: "/opt/actions-runners",
	}
	f := FieldsFromOrgConfig(oc)
	if f.KeyMode != KeyModeKeep {
		t.Fatalf("edit default key mode = %q, want keep", f.KeyMode)
	}
	if f.CurrentKeyPath != oc.PrivateKeyPath {
		t.Fatalf("CurrentKeyPath = %q, want %q", f.CurrentKeyPath, oc.PrivateKeyPath)
	}
	if got := f.ToOrgConfig(); !reflect.DeepEqual(got, oc) {
		t.Fatalf("keep-mode round-trip = %+v, want %+v", got, oc)
	}

	// A store-based org (empty path): keep leaves it empty; so does a re-key by paste.
	store := FieldsFromOrgConfig(config.OrgConfig{Name: "x", AppID: 1, InstallationID: 2, DefaultGroupID: 1})
	if store.ToOrgConfig().PrivateKeyPath != "" {
		t.Fatal("keep on a store-based org must leave PrivateKeyPath empty")
	}
	store.KeyMode = KeyModePaste
	if store.ToOrgConfig().PrivateKeyPath != "" {
		t.Fatal("paste must leave PrivateKeyPath empty")
	}
	// Switching keep -> path uses the (new) KeyPath.
	p := FieldsFromOrgConfig(oc)
	p.KeyMode, p.KeyPath = KeyModePath, "/new/key.pem"
	if got := p.ToOrgConfig().PrivateKeyPath; got != "/new/key.pem" {
		t.Fatalf("path mode PrivateKeyPath = %q, want /new/key.pem", got)
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
		if got := SplitCSV(in); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitCSV(%q) = %v, want %v", in, got, want)
		}
	}
}

// Editing an org through the form must keep every field the form does not show
// (profiles, per-org resources, runnerUser), not reset them, and must round-trip
// the dind port partition.
func TestEditPreservesUnmanagedFields(t *testing.T) {
	oc := config.OrgConfig{
		Name: "acme", AppID: 123, InstallationID: 456, DefaultGroupID: 7,
		DefaultLabels: []string{"self-hosted"}, InstallRoot: "/opt/actions-runners",
		RunnerUser: "svc-acme",
		Profiles:   []core.RunnerProfile{{Name: "dind", Labels: []string{"docker"}, GroupID: 7, Ephemeral: true}},
		Resources:  config.ResourceLimits{MemoryMax: "8G"},
		DinD:       config.OrgDinD{PortRange: "10000-29999", PortsPerSlot: 500},
	}
	f := FieldsFromOrgConfig(oc)
	if f.DinDPortRange != "10000-29999" || f.DinDPortsPerSlot != "500" {
		t.Fatalf("dind fields not seeded: %q %q", f.DinDPortRange, f.DinDPortsPerSlot)
	}
	if got := f.ToOrgConfig(); !reflect.DeepEqual(got, oc) {
		t.Fatalf("untouched edit changed the org:\n got %+v\nwant %+v", got, oc)
	}

	// Change only the labels and clear the partition: the rest survives.
	f.LabelsStr, f.DinDPortRange, f.DinDPortsPerSlot = "self-hosted,linux", "", ""
	got := f.ToOrgConfig()
	if got.RunnerUser != "svc-acme" || len(got.Profiles) != 1 || got.Resources.MemoryMax != "8G" {
		t.Fatalf("edit dropped unmanaged fields: %+v", got)
	}
	if !got.DinD.IsZero() {
		t.Fatalf("clearing the pool must clear the dind block: %+v", got.DinD)
	}
	if !reflect.DeepEqual(got.DefaultLabels, []string{"self-hosted", "linux"}) {
		t.Fatalf("labels edit not applied: %v", got.DefaultLabels)
	}
}

func TestDinDFieldValidation(t *testing.T) {
	for _, ok := range []string{"", "  ", "10000-29999"} {
		if err := validDinDPool(ok); err != nil {
			t.Errorf("pool %q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"10000", "500-600", "x-y"} {
		if validDinDPool(bad) == nil {
			t.Errorf("pool %q accepted", bad)
		}
	}
	if err := validDinDPerSlot("10000-29999", ""); err != nil {
		t.Errorf("blank block size rejected: %v", err)
	}
	if err := validDinDPerSlot("10000-29999", "500"); err != nil {
		t.Errorf("valid block size rejected: %v", err)
	}
	for _, bad := range []string{"0", "-1", "abc", "20001"} {
		if validDinDPerSlot("10000-29999", bad) == nil {
			t.Errorf("block size %q accepted for a 20000-port pool", bad)
		}
	}
}
