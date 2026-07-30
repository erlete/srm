package secrets

import (
	"path/filepath"
	"testing"
)

// PersistPassphrase writes a passphrase that PassphraseFromFile reads back, and
// ResolvePassphrase prefers the environment over the file.
func TestPassphrasePersistResolve(t *testing.T) {
	t.Setenv("SRM_SECRETS_PASSPHRASE", "")
	dir := t.TempDir()
	pp := filepath.Join(dir, "secrets.pass")

	if _, ok := PassphraseFromFile(pp); ok {
		t.Fatal("an absent passphrase file must report not-found")
	}
	if p, ok := ResolvePassphrase(pp); ok {
		t.Fatalf("ResolvePassphrase with no env and no file should be empty, got %q", p)
	}
	if err := PersistPassphrase(pp, "hunter2hunter2"); err != nil {
		t.Fatalf("PersistPassphrase: %v", err)
	}
	if p, ok := PassphraseFromFile(pp); !ok || p != "hunter2hunter2" {
		t.Fatalf("PassphraseFromFile = %q,%v; want the persisted value", p, ok)
	}
	if p, ok := ResolvePassphrase(pp); !ok || p != "hunter2hunter2" {
		t.Fatalf("ResolvePassphrase should fall back to the file: %q,%v", p, ok)
	}
	t.Setenv("SRM_SECRETS_PASSPHRASE", "envwins")
	if p, ok := ResolvePassphrase(pp); !ok || p != "envwins" {
		t.Fatalf("ResolvePassphrase must prefer the env var: %q,%v", p, ok)
	}
}

// EncryptAppKey errors when no passphrase is available and none is supplied, then on
// a supplied passphrase persists it (root-only) and stores the key so a second call
// reuses the persisted passphrase without a new one.
func TestEncryptAppKeyRoundTrip(t *testing.T) {
	t.Setenv("SRM_SECRETS_PASSPHRASE", "")
	dir := t.TempDir()
	sec := filepath.Join(dir, "secrets.age")
	pass := filepath.Join(dir, "secrets.pass")

	if _, err := EncryptAppKey(sec, pass, "acme", "PEMDATA", ""); err == nil {
		t.Fatal("expected an error when no passphrase is available and none is supplied")
	}
	st, err := EncryptAppKey(sec, pass, "acme", "PEMDATA", "created-pass-123")
	if err != nil {
		t.Fatalf("EncryptAppKey with a new passphrase: %v", err)
	}
	if got, err := st.Get("app_key:acme"); err != nil || got != "PEMDATA" {
		t.Fatalf("stored key = %q,%v; want PEMDATA", got, err)
	}
	if p, ok := PassphraseFromFile(pass); !ok || p != "created-pass-123" {
		t.Fatalf("passphrase not persisted: %q,%v", p, ok)
	}
	if _, err := EncryptAppKey(sec, pass, "globex", "PEM2", ""); err != nil {
		t.Fatalf("second store should reuse the persisted passphrase: %v", err)
	}
}
