//go:build windows

package runner

import (
	"reflect"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// TestSetMultiStringValueRoundTrip exercises the real REG_MULTI_SZ write/read on this
// box via a throwaway HKCU key (no elevation), validating the exact SetStringsValue/
// GetStringsValue path writeServiceEnvironment uses against the HKLM service key. The
// production path differs only in root+location (HKLM\SYSTEM\...\Services\<svc>), which
// needs Administrator and is covered by the live host integration test.
func TestSetMultiStringValueRoundTrip(t *testing.T) {
	const sub = `Software\srm-regtest-multistring`
	k, _, err := registry.CreateKey(registry.CURRENT_USER, sub, registry.ALL_ACCESS)
	if err != nil {
		t.Fatalf("create test key: %v", err)
	}
	k.Close()
	defer registry.DeleteKey(registry.CURRENT_USER, sub)

	want := []string{
		`AGENT_TOOLSDIRECTORY=C:\hostedtoolcache`,
		`GOMODCACHE=C:\ProgramData\srm\cache\go\mod`,
	}
	if err := setMultiStringValue(registry.CURRENT_USER, sub, "Environment", want); err != nil {
		t.Fatalf("setMultiStringValue: %v", err)
	}

	rk, err := registry.OpenKey(registry.CURRENT_USER, sub, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer rk.Close()
	got, _, err := rk.GetStringsValue("Environment")
	if err != nil {
		t.Fatalf("GetStringsValue: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch: got %v want %v", got, want)
	}
}
