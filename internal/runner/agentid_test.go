package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// The GitHub Actions agent (.NET) writes .runner as UTF-8 WITH a byte-order mark.
// AgentID must strip it before JSON-decoding, or deregister-by-id fails and the
// runner is left registered on GitHub (regression: BOM seen live on ARMORA).
func TestAgentIDStripsBOM(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "DLT-Code", "srm-ci-armora-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Build the BOM from raw bytes so this source file stays plain ASCII.
	bom := string([]byte{0xEF, 0xBB, 0xBF})
	content := bom + `{"agentId": 269, "agentName": "srm-ci-armora-1"}`
	if err := os.WriteFile(filepath.Join(dir, ".runner"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	orch := NewUbuntu(root, Options{Org: "DLT-Code"})
	id, err := orch.AgentID("DLT-Code", "srm-ci-armora-1")
	if err != nil {
		t.Fatalf("AgentID with BOM: %v", err)
	}
	if id != 269 {
		t.Fatalf("id = %d, want 269", id)
	}
}
