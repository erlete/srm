package service

import (
	"path/filepath"
	"testing"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/secrets"
)

// StoreOrgKey persists a freshly-created passphrase, encrypts the key, and swaps the
// Manager onto the age store so the key resolves immediately (no restart).
func TestManagerStoreOrgKey(t *testing.T) {
	t.Setenv("SRM_SECRETS_PASSPHRASE", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	m := New(&config.Config{Orgs: []config.OrgConfig{{Name: "acme"}}}, secrets.EnvStore{}, cfgPath)

	if m.HasSecretsPassphrase() {
		t.Fatal("no passphrase should exist yet")
	}
	if err := m.StoreOrgKey("acme", "PEMDATA", "created-pass-123"); err != nil {
		t.Fatalf("StoreOrgKey: %v", err)
	}
	if !m.HasSecretsPassphrase() {
		t.Fatal("passphrase should be persisted after StoreOrgKey")
	}
	// The manager adopted the age store: the key resolves through m.sec (was EnvStore).
	if got, err := m.sec.Get("app_key:acme"); err != nil || got != "PEMDATA" {
		t.Fatalf("m.sec key = %q,%v; want PEMDATA via the adopted age store", got, err)
	}
}
