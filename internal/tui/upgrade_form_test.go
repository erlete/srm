package tui

import "testing"

// The optional-version validator is a shape check: blank is allowed (use the
// default), a dotted numeric version is allowed (a leading "v" tolerated), and
// anything non-numeric or malformed is rejected before the dry-run runs.
func TestValidateOptionalAgentVersion(t *testing.T) {
	ok := []string{"", "  ", "2.335.1", "v2.335.1", "2.340", "2", "10.20.30.40"}
	for _, s := range ok {
		if err := validateOptionalAgentVersion(s); err != nil {
			t.Errorf("validateOptionalAgentVersion(%q) = %v, want nil", s, err)
		}
	}
	bad := []string{"2.x", "abc", "2.335.", ".2.3", "2..3", "v", "2.3.-1", "2 .3"}
	for _, s := range bad {
		if err := validateOptionalAgentVersion(s); err == nil {
			t.Errorf("validateOptionalAgentVersion(%q) = nil, want an error", s)
		}
	}
}

// A completed form yields a normalized ToVersion (trimmed, leading "v" stripped so
// DownloadURL's own "v" isn't doubled) and passes Force through verbatim.
func TestUpgradeFormNormalizesToVersion(t *testing.T) {
	uf := newUpgradeForm()
	uf.version, uf.force = "  v2.340.0 ", true
	if got := uf.toVersion(); got != "2.340.0" {
		t.Fatalf("toVersion() = %q, want %q", got, "2.340.0")
	}
	if !uf.force {
		t.Fatal("force not carried through the form")
	}

	uf2 := newUpgradeForm()
	if got := uf2.toVersion(); got != "" {
		t.Fatalf("blank version toVersion() = %q, want \"\" (default)", got)
	}
}

// startUpgrade opens the options form on an elevated host, and refuses (no form)
// when the session can't mutate the host.
func TestStartUpgradeOpensFormOnlyWhenHostCapable(t *testing.T) {
	m := sizedModel(t)
	m.hostCapable = true
	tm, cmd := m.startUpgrade()
	m = tm.(Model)
	if m.upgradeForm == nil {
		t.Fatal("startUpgrade did not open the upgrade options form")
	}
	if cmd == nil {
		t.Fatal("startUpgrade returned no form Init cmd")
	}

	m2 := sizedModel(t)
	m2.hostCapable = false
	tm2, _ := m2.startUpgrade()
	if tm2.(Model).upgradeForm != nil {
		t.Fatal("startUpgrade opened the form without host capability")
	}
}
