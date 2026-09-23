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

// A passphrase supplied only via SRM_SECRETS_PASSPHRASE must be PERSISTED to the
// passphrase file, so a detached ephemeral unit (which has no env var) can decrypt
// the store on its own. Regression test for a headless import that left no
// secrets.pass and so an undecryptable store.
func TestEncryptAppKeyPersistsEnvPassphrase(t *testing.T) {
	dir := t.TempDir()
	sec := filepath.Join(dir, "secrets.age")
	pass := filepath.Join(dir, "secrets.pass")
	t.Setenv("SRM_SECRETS_PASSPHRASE", "env-supplied-pass-xyz")

	if _, err := EncryptAppKey(sec, pass, "acme", "PEMDATA", ""); err != nil {
		t.Fatalf("EncryptAppKey with an env passphrase: %v", err)
	}
	if p, ok := PassphraseFromFile(pass); !ok || p != "env-supplied-pass-xyz" {
		t.Fatalf("env passphrase not persisted to file: %q,%v", p, ok)
	}

	// Prove a fresh store WITHOUT the env var (like a detached unit) can still open it.
	t.Setenv("SRM_SECRETS_PASSPHRASE", "")
	filePass, ok := PassphraseFromFile(pass)
	if !ok {
		t.Fatal("passphrase file unreadable")
	}
	st, err := NewAgeFileStore(sec, filePass)
	if err != nil {
		t.Fatalf("reopen store with the persisted passphrase: %v", err)
	}
	if got, err := st.Get("app_key:acme"); err != nil || got != "PEMDATA" {
		t.Fatalf("decrypted key = %q,%v; want PEMDATA", got, err)
	}
}
