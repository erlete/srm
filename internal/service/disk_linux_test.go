//go:build linux

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/erlete/srm/internal/config"
	ghub "github.com/erlete/srm/internal/github"
)

// A slot tree with no unit (an interrupted create) is reported as a leftover,
// with its size; the filesystem's free space comes back too.
func TestDiskUsageFindsLeftoverSlotTrees(t *testing.T) {
	root := t.TempDir()
	slot := filepath.Join(root, "acme", ".ephemeral", "4")
	if err := os.MkdirAll(filepath.Join(slot, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slot, "bin", "Runner.Listener"), make([]byte, 64<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "acme", ".ephemeral", "not-a-slot"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Manager{
		cfg:     &config.Config{Orgs: []config.OrgConfig{{Name: "acme", InstallRoot: root}}, MinFreeDisk: "1K"},
		clients: map[string]ghub.Client{},
	}
	rep := m.DiskUsage(context.Background(), []string{"acme"})
	if rep.Err != "" || rep.Total == 0 || rep.MinFree != 1024 {
		t.Fatalf("filesystem: %+v", rep)
	}
	if len(rep.Slots) != 1 {
		t.Fatalf("slots: %+v", rep.Slots)
	}
	s := rep.Slots[0]
	if s.Org != "acme" || s.Slot != "4" || !s.Orphan || s.Bytes < 64<<10 || s.Partial {
		t.Errorf("slot: %+v", s)
	}
}
