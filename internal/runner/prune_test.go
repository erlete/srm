package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/erlete/srm/internal/config"
)

// Install-tree cache dirs (config.InstallTreeCacheDirs) are never evicted file by
// file: a half-deleted browser or package manager still looks installed.
func TestPruneDepCacheKeepsInstallTrees(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-90 * 24 * time.Hour)
	write := func(rel string) string {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stale := write("npm/_cacache/stale")
	kept := []string{
		write("playwright/chromium-1140/locales/fr.pak"),
		write("corepack/v1/pnpm/9.0.0/dist/worker.js"),
		write("uv/archive-v0/abc/pkg/__init__.py"),
		write("tools/mise/installs/node/22/bin/node"), // MISE_DATA_DIR moved by cache.env
	}
	u := &ubuntu{opts: Options{
		CacheRoot: root,
		PruneKeep: config.InstallTreeCacheDirs(filepath.ToSlash(root), map[string]string{"MISE_DATA_DIR": "tools/mise"}),
	}}
	st, err := u.PruneDepCache(context.Background(), 30*24*time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale npm file not pruned (stats %+v)", st)
	}
	for _, p := range kept {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("install-tree file pruned: %s", p)
		}
	}
	if st.Files != 1 {
		t.Errorf("pruned %d files, want 1", st.Files)
	}
}
