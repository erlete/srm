// Package fsown fixes file ownership safely next to live jobs.
package fsown

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// Directory names ChownTree never enters.
var (
	// A slot's rootless Docker data-root: its files belong to the user namespace's
	// subordinate ids by design, and handing them to the runner user would make
	// them root-owned inside the containers (a rootless PostgreSQL loses its data,
	// buildx's cache changes owner).
	SkipDockerData = map[string]bool{".docker-data": true}
	// An org tree walk also leaves the ephemeral slots alone: each slot's own setup
	// owns its tree, and live slots write (and wipe) it while the walk runs.
	SkipSlotsAndDockerData = map[string]bool{".ephemeral": true, ".docker-data": true}
)

// Seams for tests.
var (
	lchownFn    = os.Lchown
	statOwnerFn = statOwner
	lookupIDsFn = lookupIDs
)

func lookupIDs(name string) (uid, gid int, err error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	if uid, err = strconv.Atoi(u.Uid); err != nil {
		return 0, 0, fmt.Errorf("user %s: uid %q: %w", name, u.Uid, err)
	}
	if gid, err = strconv.Atoi(u.Gid); err != nil {
		return 0, 0, fmt.Errorf("user %s: gid %q: %w", name, u.Gid, err)
	}
	return uid, gid, nil
}

// ChownTree gives root and everything below it to owner (user and primary group),
// as `chown -R` did, but safe to run next to live jobs:
//   - entries already owned by owner are left alone (normally all of them, so a
//     re-run writes nothing);
//   - symbolic links are never followed (lchown);
//   - a non-directory with more than one hard link that owner does not own is
//     skipped: it may be a link to a file outside the tree, and chowning it would
//     hand that file to the runner user;
//   - directories named in skip are not entered (see SkipDockerData and
//     SkipSlotsAndDockerData);
//   - entries that disappear during the walk are ignored (jobs keep writing).
//
// Other errors do not stop the walk; the first one is returned at the end.
func ChownTree(root, owner string, skip map[string]bool) error {
	uid, gid, err := lookupIDsFn(owner)
	if err != nil {
		return fmt.Errorf("chown %s: %w", root, err)
	}
	var first error
	keep := func(err error) {
		if first == nil && !errors.Is(err, fs.ErrNotExist) {
			first = err
		}
	}
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			keep(err)
			return nil
		}
		if p != root && d.IsDir() && skip[d.Name()] {
			return fs.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			keep(err)
			return nil
		}
		fuid, fgid, nlink, known := statOwnerFn(info)
		if known && fuid == uid && fgid == gid {
			return nil
		}
		if known && !d.IsDir() && nlink > 1 {
			return nil
		}
		if err := lchownFn(p, uid, gid); err != nil {
			keep(err)
		}
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("chown %s: %w", root, walkErr)
	}
	if first != nil {
		return fmt.Errorf("chown %s: %w", root, first)
	}
	return nil
}
