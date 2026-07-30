package tui

import (
	"testing"

	"github.com/erlete/srm/internal/service"
	"github.com/erlete/srm/internal/setup"
)

// The onboard wizard collects into the shared setup.OrgFields and converts to an
// OrgConfig on completion. Path mode keeps the .pem reference in PrivateKeyPath.
func TestOnboardFormMapsFields(t *testing.T) {
	of := newOnboardForm("/etc/srm", true)
	of.fields.Name = "acme"
	of.fields.AppIDStr = "123"
	of.fields.InstIDStr = "456"
	of.fields.KeyMode = setup.KeyModePath
	of.fields.KeyPath = "/etc/srm/acme.pem"
	oc := of.fields.ToOrgConfig()
	if oc.Name != "acme" || oc.AppID != 123 || oc.InstallationID != 456 || oc.PrivateKeyPath != "/etc/srm/acme.pem" {
		t.Fatalf("onboard fields not mapped: %+v", oc)
	}
}

// The restore picker clamps its cursor and reports the highlighted archive.
func TestRestorePickerMoveSelect(t *testing.T) {
	p := restorePicker{theme: NewTheme(), backups: []service.BackupInfo{
		{Name: "srm-backup-3.tar.gz", Path: "/b/3"},
		{Name: "srm-backup-2.tar.gz", Path: "/b/2"},
		{Name: "srm-backup-1.tar.gz", Path: "/b/1"},
	}}
	p.move(-1) // clamp at top
	if b, _ := p.selected(); b.Path != "/b/3" {
		t.Fatalf("cursor should clamp at 0, got %q", b.Path)
	}
	p.move(1)
	p.move(1)
	p.move(1) // clamp at bottom (len-1)
	if b, ok := p.selected(); !ok || b.Path != "/b/1" {
		t.Fatalf("cursor should clamp at last, got %q", b.Path)
	}

	empty := restorePicker{theme: NewTheme()}
	if _, ok := empty.selected(); ok {
		t.Error("empty picker should report no selection")
	}
}

// A reloadMsg drops the cached fleet snapshot so the next fleet view reloads against
// the reloaded config, and surfaces the note (warn -> error style).
func TestReloadMsgInvalidatesFleetCache(t *testing.T) {
	m := sizedModel(t)
	m.fleetLoaded = true
	tm, _ := m.Update(reloadMsg{note: "org acme onboarded and authenticated"})
	m = tm.(Model)
	if m.fleetLoaded {
		t.Error("reloadMsg should invalidate the cached fleet snapshot")
	}
	if m.status == "" || m.stErr {
		t.Errorf("success reloadMsg should set a non-error status, got %q err=%v", m.status, m.stErr)
	}

	tm, _ = sizedModel(t).Update(reloadMsg{err: errStub})
	if m2 := tm.(Model); !m2.stErr {
		t.Error("an errored reloadMsg should set the error flag")
	}
}

var errStub = stubErr("boom")

type stubErr string

func (e stubErr) Error() string { return string(e) }
