package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erlete/srm/internal/core"
)

func TestStripFirstComponent(t *testing.T) {
	cases := map[string]string{
		"node-v22.11.0-win-x64/node.exe":       "node.exe",
		"node-v22.11.0-win-x64/node_modules/x": "node_modules/x",
		"go/bin/go.exe":                        "bin/go.exe",
		"node-v22.11.0-win-x64/":               "", // the top-level dir entry itself
		"go/":                                  "",
		"single":                               "", // no separator => nothing below the top
		"/leading/slash/file":                  "slash/file",
	}
	for in, want := range cases {
		if got := stripFirstComponent(in); got != want {
			t.Errorf("stripFirstComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToolCacheName(t *testing.T) {
	for tool, want := range map[string]string{"node": "node", "go": "go", "python": "Python"} {
		got, ok := toolCacheName(tool)
		if !ok || got != want {
			t.Errorf("toolCacheName(%q) = %q,%v want %q,true", tool, got, ok, want)
		}
	}
	if _, ok := toolCacheName("ruby"); ok {
		t.Error("toolCacheName(ruby) should be unknown")
	}
}

// TestSeedValidation covers the entries that fail BEFORE any download (bad format,
// unsupported/unknown tool) - the happy node/go paths need the network and are
// exercised live, not here.
func TestSeedValidation(t *testing.T) {
	w := &windows{toolCacheRoot: t.TempDir()}
	cases := map[string]string{
		"node":        "expected tool@version",
		"node@":       "expected tool@version",
		"python@3.12": "not yet supported on Windows",
		"ruby@3.3":    "unsupported seed tool",
	}
	for entry, wantSub := range cases {
		err := w.seed(context.Background(), entry)
		if err == nil || !strings.Contains(err.Error(), wantSub) {
			t.Errorf("seed(%q) error = %v, want containing %q", entry, err, wantSub)
		}
	}
}

// TestDriftToolCacheMarkers verifies Drift reports a seed missing iff its
// x64.complete marker is absent. No packages in the manifest, so the package-manager
// path (which needs winget/choco) is not exercised.
func TestDriftToolCacheMarkers(t *testing.T) {
	root := t.TempDir()
	// node@22.11.0 is present (marker exists); go@1.23.4 is not.
	present := filepath.Join(root, "node", "22.11.0", "x64.complete")
	if err := os.MkdirAll(filepath.Dir(present), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(present, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewWindowsFor("", root)
	missing, err := r.Drift(context.Background(), core.DependencyManifest{
		ToolCacheSeeds: []string{"node@22.11.0", "go@1.23.4"},
	})
	if err != nil {
		t.Fatalf("Drift: %v", err)
	}
	joined := strings.Join(missing, ",")
	if strings.Contains(joined, "node@22.11.0") {
		t.Errorf("node@22.11.0 has a marker but was reported missing: %v", missing)
	}
	if !strings.Contains(joined, "toolcache:go@1.23.4") {
		t.Errorf("go@1.23.4 has no marker but was not reported missing: %v", missing)
	}
}
