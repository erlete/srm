package fsown

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testUID, testGID = 1000, 1000

// fakeIDs swaps the seams: the target user is testUID:testGID, entries named in
// owned already belong to it, entries in linked have two hard links, everything
// else is foreign (root). lchown calls are recorded relative to root, and hook
// (when set) runs before each one.
func fakeIDs(t *testing.T, root string, owned, linked []string, hook func(rel string) error) *[]string {
	t.Helper()
	oldL, oldS, oldU := lchownFn, statOwnerFn, lookupIDsFn
	t.Cleanup(func() { lchownFn, statOwnerFn, lookupIDsFn = oldL, oldS, oldU })
	lookupIDsFn = func(string) (int, int, error) { return testUID, testGID, nil }
	statOwnerFn = func(fi fs.FileInfo) (int, int, uint64, bool) {
		switch {
		case slices.Contains(owned, fi.Name()):
			return testUID, testGID, 1, true
		case slices.Contains(linked, fi.Name()):
			return 0, 0, 2, true
		}
		return 0, 0, 1, true
	}
	var calls []string
	lchownFn = func(p string, uid, gid int) error {
		if uid != testUID || gid != testGID {
			t.Errorf("lchown %s to %d:%d", p, uid, gid)
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		calls = append(calls, rel)
		if hook != nil {
			return hook(rel)
		}
		return nil
	}
	return &calls
}

func mkfile(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The org tree walk (EnsureBase) next to live slots: it must never enter another
// slot's tree or any rootless Docker data-root, must leave owned and multiply
// linked files alone, and must survive files vanishing while jobs run.
func TestChownTreeOrgWalk(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"runner-1/a-trigger",
		"runner-1/bin/run.sh",
		"runner-1/owned",
		"runner-1/hard",
		"runner-1/gone",
		"runner-1/zz-vanish",
		"runner-1/_work/.docker-data/vol/x",
		".ephemeral/1/.docker-data/volumes/buildx/_data/f",
		".ephemeral/1/_work/job/file",
	} {
		mkfile(t, root, rel)
	}
	calls := fakeIDs(t, root, []string{"owned"}, []string{"hard"}, func(rel string) error {
		switch rel {
		case "runner-1/a-trigger": // a job deletes a sibling after the dir was listed
			return os.Remove(filepath.Join(root, "runner-1", "zz-vanish"))
		case "runner-1/gone":
			return &fs.PathError{Op: "lchown", Path: rel, Err: fs.ErrNotExist}
		}
		// Like a real lchown: a path that vanished fails with ENOENT. (Off Linux the
		// walk may still reach it, since ReadDir there caches the file info.)
		_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		return err
	})

	if err := ChownTree(root, "srm-acme", SkipSlotsAndDockerData); err != nil {
		t.Fatalf("ChownTree: %v", err)
	}
	for _, c := range *calls {
		if strings.Contains(c, ".docker-data") || strings.HasPrefix(c, ".ephemeral") {
			t.Errorf("entered a skipped tree: %s", c)
		}
	}
	for _, want := range []string{".", "runner-1", "runner-1/a-trigger", "runner-1/bin", "runner-1/bin/run.sh", "runner-1/_work"} {
		if !slices.Contains(*calls, want) {
			t.Errorf("not chowned: %s (calls %v)", want, *calls)
		}
	}
	for _, skip := range []string{"runner-1/owned", "runner-1/hard"} {
		if slices.Contains(*calls, skip) {
			t.Errorf("must not chown %s", skip)
		}
	}
}

// A slot's own tree walk (EnsureEphemeralSlot) does enter the slot, but still not
// its Docker data-root.
func TestChownTreeSlotWalk(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "bin/Runner.Listener")
	mkfile(t, root, ".docker-data/overlay/f")
	calls := fakeIDs(t, root, nil, nil, nil)
	if err := ChownTree(root, "srm-acme", SkipDockerData); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(*calls, "bin/Runner.Listener") {
		t.Errorf("slot files not chowned: %v", *calls)
	}
	for _, c := range *calls {
		if strings.HasPrefix(c, ".docker-data") {
			t.Errorf("entered the docker data-root: %s", c)
		}
	}
}

// A real failure is reported, but the walk still finishes the rest of the tree.
func TestChownTreeReportsErrorsAndContinues(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "a-denied")
	mkfile(t, root, "b-fine")
	denied := errors.New("operation not permitted")
	calls := fakeIDs(t, root, nil, nil, func(rel string) error {
		if rel == "a-denied" {
			return denied
		}
		return nil
	})
	err := ChownTree(root, "srm-acme", nil)
	if !errors.Is(err, denied) {
		t.Fatalf("err = %v, want the lchown failure", err)
	}
	if !slices.Contains(*calls, "b-fine") {
		t.Errorf("walk stopped at the first error: %v", *calls)
	}
}

// A missing root is not an error (nothing to fix yet).
func TestChownTreeMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	fakeIDs(t, root, nil, nil, nil)
	if err := ChownTree(root, "srm-acme", nil); err != nil {
		t.Fatalf("missing root: %v", err)
	}
}
