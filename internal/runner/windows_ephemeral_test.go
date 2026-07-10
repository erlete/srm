package runner

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestEphemeralSvcName locks the ephemeral supervisor service name contract:
// "actions.ephemeral.<org>.<slot>" with NO ".service" suffix (that systemd-ism is
// appended only by ListEphemeralUnits for the reconcile name parser).
func TestEphemeralSvcName(t *testing.T) {
	w := &windows{installRoot: `C:\actions-runners`}
	got := w.ephemeralSvcName("DLT-Code", "1")
	want := "actions.ephemeral.DLT-Code.1"
	if got != want {
		t.Errorf("ephemeralSvcName = %q, want %q", got, want)
	}
	if strings.HasSuffix(got, ".service") {
		t.Error("ephemeral service name must not carry the systemd .service suffix")
	}
}

// TestEphemeralSlotDir confirms the slot tree layout matches the Linux mirror
// ({installRoot}/{org}/.ephemeral/{slot}) the reconcile and teardown paths assume.
func TestEphemeralSlotDir(t *testing.T) {
	w := &windows{installRoot: `C:\actions-runners`}
	got := w.ephemeralSlotDir("DLT-Code", "2")
	want := `C:\actions-runners\DLT-Code\.ephemeral\2`
	if got != want {
		t.Errorf("ephemeralSlotDir = %q, want %q", got, want)
	}
}

// TestSCAccount locks the `sc create obj=` logon-account spelling: the built-in
// passwordless accounts go in as their canonical form (the display-name form is not
// reliably accepted as a service logon account), and a real account passes verbatim.
func TestSCAccount(t *testing.T) {
	cases := map[string]string{
		`NT AUTHORITY\NETWORK SERVICE`: `NT AUTHORITY\NetworkService`,
		`network service`:              `NT AUTHORITY\NetworkService`,
		`LOCAL SERVICE`:                `NT AUTHORITY\LocalService`,
		`SYSTEM`:                       `LocalSystem`,
		`LocalSystem`:                  `LocalSystem`,
		`acme\ci-runner`:               `acme\ci-runner`,
	}
	for in, want := range cases {
		if got := scAccount(in); got != want {
			t.Errorf("scAccount(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEphemeralJITLedger exercises the RecordJIT -> PendingJIT -> ClearJIT round-trip
// (the crash-reapable ghost ledger) against a temp control root. RecordJIT must
// persist the id, PendingJIT read it back, ClearJIT remove it, and ClearJIT be a
// no-op on an absent id.
func TestEphemeralJITLedger(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir()) // DataRoot() reads %ProgramData% dynamically
	w := &windows{installRoot: `C:\actions-runners`}
	org, slot := "DLT-Code", "1"

	if id := w.PendingJIT(org, slot); id != 0 {
		t.Fatalf("PendingJIT on empty ledger = %d, want 0", id)
	}
	if err := w.RecordJIT(org, slot, 4242); err != nil {
		t.Fatalf("RecordJIT: %v", err)
	}
	if id := w.PendingJIT(org, slot); id != 4242 {
		t.Errorf("PendingJIT after record = %d, want 4242", id)
	}
	if err := w.ClearJIT(org, slot); err != nil {
		t.Fatalf("ClearJIT: %v", err)
	}
	if id := w.PendingJIT(org, slot); id != 0 {
		t.Errorf("PendingJIT after clear = %d, want 0", id)
	}
	if err := w.ClearJIT(org, slot); err != nil {
		t.Errorf("ClearJIT on absent id should be a no-op, got %v", err)
	}
}

// TestEphemeralMintParamsRoundTrip confirms the persisted JIT mint params decode back
// to the same group + labels each cycle reads.
func TestEphemeralMintParamsRoundTrip(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	w := &windows{installRoot: `C:\actions-runners`}
	org, slot := "DLT-Code", "3"
	if err := os.MkdirAll(w.ephemeralControlDir(org, slot), 0o700); err != nil {
		t.Fatal(err)
	}
	want := EphemeralParams{GroupID: 7, Labels: []string{"win", "x64"}}
	b, _ := json.Marshal(want)
	if err := os.WriteFile(w.jitParamsPath(org, slot), b, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := w.EphemeralMintParams(org, slot)
	if err != nil {
		t.Fatalf("EphemeralMintParams: %v", err)
	}
	if got.GroupID != want.GroupID || len(got.Labels) != 2 || got.Labels[0] != "win" || got.Labels[1] != "x64" {
		t.Errorf("params = %+v, want %+v", got, want)
	}
}

// TestEphemeralCycleCounter confirms the srm-tracked cycle counter (the Windows
// NRestarts stand-in) starts at 0 and increments monotonically.
func TestEphemeralCycleCounter(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())
	org, slot := "DLT-Code", "1"
	if n := EphemeralCycleCount(org, slot); n != 0 {
		t.Fatalf("initial cycle count = %d, want 0", n)
	}
	IncrementEphemeralCycle(org, slot)
	IncrementEphemeralCycle(org, slot)
	IncrementEphemeralCycle(org, slot)
	if n := EphemeralCycleCount(org, slot); n != 3 {
		t.Errorf("cycle count after 3 increments = %d, want 3", n)
	}
}
