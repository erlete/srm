package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/secrets"
)

// UpsertOrg inserts a new org and replaces an existing one in place (matched by name).
func TestManagerUpsertOrgReplaces(t *testing.T) {
	m := New(&config.Config{}, secrets.EnvStore{}, "")
	m.UpsertOrg(config.OrgConfig{Name: "acme", AppID: 1})
	m.UpsertOrg(config.OrgConfig{Name: "globex", AppID: 2})
	m.UpsertOrg(config.OrgConfig{Name: "acme", AppID: 9}) // replace, not append
	if len(m.Config().Orgs) != 2 {
		t.Fatalf("want 2 orgs after upsert, got %d", len(m.Config().Orgs))
	}
	if oc, _ := m.Config().Org("acme"); oc.AppID != 9 {
		t.Errorf("acme not replaced: AppID = %d, want 9", oc.AppID)
	}
}

// Reload re-reads the on-disk config, discarding unsaved in-memory edits, and errors
// when no config path is set.
func TestManagerReloadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	m := New(&config.Config{}, secrets.EnvStore{}, path)

	m.UpsertOrg(config.OrgConfig{Name: "acme", AppID: 1, InstallationID: 2, PrivateKeyPath: "/k.pem"})
	if err := m.SaveConfig(); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	// An unsaved in-memory edit must be dropped by Reload (re-reads the saved file).
	m.UpsertOrg(config.OrgConfig{Name: "acme", AppID: 99})
	if err := m.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := m.OrgNames(); len(got) != 1 || got[0] != "acme" {
		t.Fatalf("orgs after reload = %v, want [acme]", got)
	}
	if oc, _ := m.Config().Org("acme"); oc.AppID != 1 {
		t.Errorf("reload should restore the saved AppID 1, got %d", oc.AppID)
	}

	if err := New(&config.Config{}, secrets.EnvStore{}, "").Reload(); err == nil {
		t.Error("Reload with no config path should error")
	}
}

// ListBackups finds the srm-backup-*/srm-config-* archives, ignores other files,
// sorts newest first, and treats a missing dir as empty.
func TestListBackups(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("srm-backup-100.tar.gz")
	write("srm-config-200.tar.gz")
	write("notes.txt")     // wrong extension - ignored
	write("random.tar.gz") // wrong prefix - ignored

	// Distinct mtimes so the newest-first ordering is deterministic.
	_ = os.Chtimes(filepath.Join(dir, "srm-backup-100.tar.gz"), time.Unix(1000, 0), time.Unix(1000, 0))
	_ = os.Chtimes(filepath.Join(dir, "srm-config-200.tar.gz"), time.Unix(2000, 0), time.Unix(2000, 0))

	got, err := ListBackups(dir)
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 archives, got %d: %+v", len(got), got)
	}
	if got[0].Name != "srm-config-200.tar.gz" {
		t.Errorf("newest first: got[0] = %q, want srm-config-200.tar.gz", got[0].Name)
	}

	if b, err := ListBackups(filepath.Join(dir, "does-not-exist")); err != nil || len(b) != 0 {
		t.Errorf("missing dir should be (empty, nil), got %v / %v", b, err)
	}
}
