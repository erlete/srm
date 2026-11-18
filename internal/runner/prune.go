package runner

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// PruneStats summarizes a cache prune pass.
type PruneStats struct {
	Root  string // the cache root that was scanned
	Files int64  // regular files removed (or that would be, on dry-run)
	Bytes int64  // total size of those files
}

// PruneDepCache removes build-tool dependency-cache files under the host cache
// root (/opt/srm-cache) that have not been accessed within maxAge, freeing disk
// without bounding growth on GitHub's behalf. On dry-run it only tallies. It
// deletes files (not directories): the package managers re-fetch any pruned
// entry on next use, so file-level eviction is safe. The shared tool cache
// (/opt/hostedtoolcache) is deliberately NOT pruned here - it is bounded by the
// number of toolchain versions and holds intentional seeds; pruning it safely
// needs version-directory granularity (atomic, marker-aware), a separate concern.
// For the same reason the cache dirs in opts.PruneKeep (install trees, see
// config.InstallTreeCacheDirs) are skipped.
func (u *ubuntu) PruneDepCache(_ context.Context, maxAge time.Duration, dryRun bool) (PruneStats, error) {
	stats := PruneStats{Root: u.opts.CacheRoot}
	if u.opts.CacheRoot == "" {
		return stats, nil
	}
	if _, err := os.Stat(u.opts.CacheRoot); err != nil {
		return stats, nil // nothing provisioned yet
	}
	cutoff := time.Now().Add(-maxAge)
	keep := make(map[string]bool, len(u.opts.PruneKeep))
	for _, k := range u.opts.PruneKeep {
		keep[filepath.Clean(k)] = true
	}
	err := filepath.WalkDir(u.opts.CacheRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, keep going
		}
		if d.IsDir() {
			if keep[filepath.Clean(p)] {
				return fs.SkipDir // an install tree: evicting single files would break it
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if accessTime(fi).After(cutoff) {
			return nil // recently used - keep
		}
		stats.Files++
		stats.Bytes += fi.Size()
		if !dryRun {
			_ = os.Remove(p)
		}
		return nil
	})
	return stats, err
}
