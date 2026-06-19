package secrets

import (
	"path/filepath"
	"testing"
)

func TestRedact(t *testing.T) {
	if got := Redact(""); got != "" {
		t.Fatalf("empty: %q", got)
	}
	if got := Redact("short"); got != "********" {
		t.Fatalf("short: %q", got)
	}
	if got := Redact("ghp_abcdefghijklmnop"); got == "ghp_abcdefghijklmnop" {
		t.Fatalf("token not redacted: %q", got)
	}
}

func TestAgeFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.age")

	s, err := NewAgeFileStore(path, "correct-horse")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Set("pat:acme", "ghp_secret"); err != nil {
		t.Fatalf("set: %v", err)
	}
	v, err := s.Get("pat:acme")
	if err != nil || v != "ghp_secret" {
		t.Fatalf("get = %q, %v", v, err)
	}

	// Reopening with the same passphrase must read the stored value.
	s2, err := NewAgeFileStore(path, "correct-horse")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if v, _ := s2.Get("pat:acme"); v != "ghp_secret" {
		t.Fatalf("reopen get = %q", v)
	}

	// A wrong passphrase must fail to open an existing store.
	if _, err := NewAgeFileStore(path, "wrong"); err == nil {
		t.Fatal("expected error opening with wrong passphrase")
	}
}
