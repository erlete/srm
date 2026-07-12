package setup

import (
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
