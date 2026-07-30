// Package joblog is the durable store for ephemeral job logs. An ephemeral lane wipes
// its _diag every cycle, so a job's log is gone the moment the next job starts. srm
// copies each finished job's log here (root-owned) BEFORE that wipe, so the log
// outlives the ephemeral churn and GitHub's own retention. It is a leaf package
// (stdlib only) shared by the capture path (service) and the readers (CLI/TUI).
package joblog

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Meta is the sidecar for one captured job run (<runnerID>.json next to
// <runnerID>.log). It is everything srm knows locally without a GitHub call; the
// Runs screen enriches it with repo/workflow on demand.
type Meta struct {
	Org          string `json:"org"`
	Slot         string `json:"slot"`
	RunnerName   string `json:"runnerName"`
	RunnerID     int64  `json:"runnerID"`
	StartedUnix  int64  `json:"startedUnix"`
	EndedUnix    int64  `json:"endedUnix"`
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
	HasLog       bool   `json:"hasLog"`
	CapturedUnix int64  `json:"capturedUnix"`
}

// Started is the job's start time. Duration is end-start (0 if either is unset).
func (m Meta) Started() time.Time { return time.Unix(m.StartedUnix, 0) }

func (m Meta) Duration() time.Duration {
	if m.StartedUnix == 0 || m.EndedUnix < m.StartedUnix {
		return 0
	}
	return time.Duration(m.EndedUnix-m.StartedUnix) * time.Second
}

// sortKey ranks entries newest-first (start time, falling back to capture time).
func (m Meta) sortKey() int64 {
	if m.StartedUnix > 0 {
		return m.StartedUnix
	}
	return m.CapturedUnix
}

// Store is the on-disk job-log store rooted at Root (e.g. /var/lib/srm/joblogs).
// Layout: Root/<org>/<slot>/<runnerID>.{log,json}. Root-owned 0700 so job code that
// runs as the (unprivileged) runner user can neither read another org's logs nor
// tamper with its own.
type Store struct{ Root string }

// idBase is the per-run filename stem. The GitHub runner id is stable + unique; a
// 0 id (should not happen for a minted JIT runner) degrades to a fixed stem rather
// than colliding across runs silently.
func idBase(id int64) string {
	if id > 0 {
		return strconv.FormatInt(id, 10)
	}
	return "unknown"
}

func (s Store) slotDir(org, slot string) string {
	return filepath.Join(s.Root, sanitize(org), sanitize(slot))
}

// LogPath / metaPath are the two files for one run.
func (s Store) LogPath(m Meta) string {
	return filepath.Join(s.slotDir(m.Org, m.Slot), idBase(m.RunnerID)+".log")
}

func (s Store) metaPath(m Meta) string {
	return filepath.Join(s.slotDir(m.Org, m.Slot), idBase(m.RunnerID)+".json")
}

// Write persists one run: it copies srcLog (if non-empty and readable) to the run's
// log path and writes the sidecar. A missing/unreadable source is not fatal - the
// sidecar is still written with HasLog=false so the run is recorded either way.
func (s Store) Write(m Meta, srcLog string) error {
	dir := s.slotDir(m.Org, m.Slot)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("joblog dir %s: %w", dir, err)
	}
	if srcLog != "" {
		if err := copyFile(srcLog, s.LogPath(m)); err == nil {
			m.HasLog = true
		}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.metaPath(m), b, 0o600)
}

// ReadLog returns the captured log bytes for a run (an error if it was not captured).
func (s Store) ReadLog(m Meta) ([]byte, error) { return os.ReadFile(s.LogPath(m)) }

// List returns every recorded run, newest-first. A missing Root is an empty list,
// not an error (nothing has been captured yet). Unreadable/corrupt sidecars are
// skipped rather than failing the whole listing.
func (s Store) List() ([]Meta, error) {
	if _, err := os.Stat(s.Root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil // nothing captured yet
		}
		return nil, err // e.g. permission denied - surface it (the store is root-owned)
	}
	var out []Meta
	_ = filepath.WalkDir(s.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		var m Meta
		if json.Unmarshal(b, &m) == nil && m.RunnerID != 0 {
			out = append(out, m)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].sortKey() > out[j].sortKey() })
	return out, nil
}

// Prune removes runs whose start (or capture) time is before cutoff, deleting both the
// sidecar and the log. It reports how many entries and how many log bytes were removed.
// With dryRun set it only counts. Empty slot/org dirs left behind are pruned too.
func (s Store) Prune(cutoff time.Time, dryRun bool) (entries int, bytes int64, err error) {
	all, err := s.List()
	if err != nil {
		return 0, 0, err
	}
	cut := cutoff.Unix()
	for _, m := range all {
		if m.sortKey() >= cut {
			continue
		}
		if fi, e := os.Stat(s.LogPath(m)); e == nil {
			bytes += fi.Size()
		}
		entries++
		if dryRun {
			continue
		}
		_ = os.Remove(s.LogPath(m))
		_ = os.Remove(s.metaPath(m))
	}
	if !dryRun {
		removeEmptyDirs(s.Root)
	}
	return entries, bytes, nil
}

// sanitize keeps a path segment to a single, traversal-safe component.
func sanitize(seg string) string {
	seg = strings.ReplaceAll(seg, "\\", "_")
	seg = strings.ReplaceAll(seg, "/", "_")
	if seg == "" || seg == "." || seg == ".." {
		return "_"
	}
	return seg
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// removeEmptyDirs best-effort prunes now-empty <slot>/<org> dirs under root (not root
// itself) so a long-idle host doesn't accumulate empty scaffolding.
func removeEmptyDirs(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, org := range entries {
		if !org.IsDir() {
			continue
		}
		orgDir := filepath.Join(root, org.Name())
		slots, err := os.ReadDir(orgDir)
		if err != nil {
			continue
		}
		for _, slot := range slots {
			if slot.IsDir() {
				slotDir := filepath.Join(orgDir, slot.Name())
				if isEmptyDir(slotDir) {
					_ = os.Remove(slotDir)
				}
			}
		}
		if isEmptyDir(orgDir) {
			_ = os.Remove(orgDir)
		}
	}
}

func isEmptyDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) == 0
}
