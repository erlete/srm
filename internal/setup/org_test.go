package setup

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"reflect"
	"testing"

	"github.com/erlete/srm/internal/config"
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
