package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureSubIDFile covers the rootless subuid/subgid allocator: it must be
// idempotent (a user already present is left untouched) and allocate a NON-
// overlapping block past the highest existing range, since that disjointness is the
// kernel uid-mapping isolation boundary between per-org users.
func TestEnsureSubIDFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subuid")

	// First allocation on an empty (absent) file → base block.
	if err := ensureSubIDFile(path, "srm-a"); err != nil {
		t.Fatalf("first alloc: %v", err)
	}
	if got := readFile(t, path); got != "srm-a:100000:65536\n" {
		t.Fatalf("first alloc = %q, want srm-a:100000:65536", got)
	}

	// Second user → next disjoint block (no overlap with the first).
	if err := ensureSubIDFile(path, "srm-b"); err != nil {
		t.Fatalf("second alloc: %v", err)
	}
	if want := "srm-b:165536:65536\n"; !strings.Contains(readFile(t, path), want) {
		t.Fatalf("second alloc missing %q:\n%s", want, readFile(t, path))
	}

	// Idempotent: re-running for an existing user changes nothing.
	before := readFile(t, path)
	if err := ensureSubIDFile(path, "srm-a"); err != nil {
		t.Fatalf("idempotent alloc: %v", err)
	}
	if after := readFile(t, path); after != before {
		t.Fatalf("re-alloc mutated the file:\nbefore=%q\nafter=%q", before, after)
	}
}

// TestEnsureSubIDFilePreexisting verifies a hand-managed file without a trailing
// newline is appended cleanly and the next block clears a pre-existing high range.
func TestEnsureSubIDFilePreexisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subgid")
	if err := os.WriteFile(path, []byte("root:200000:65536"), 0o644); err != nil { // no trailing \n
		t.Fatal(err)
	}
	if err := ensureSubIDFile(path, "srm-x"); err != nil {
		t.Fatalf("alloc: %v", err)
	}
	// next = max(100000, 200000+65536) = 265536, and the missing newline is repaired.
	if want := "root:200000:65536\nsrm-x:265536:65536\n"; readFile(t, path) != want {
		t.Fatalf("got %q, want %q", readFile(t, path), want)
	}
}

// TestEnsureSubIDFileRejectsUndersized covers the fix: a pre-existing range narrower
// than SubIDCount cannot map the full userns, so the allocator must FAIL LOUD rather
// than silently leave it (which would surface as a mid-job rootless dockerd failure).
// An adequate (>= SubIDCount) pre-existing range is still accepted idempotently.
func TestEnsureSubIDFileRejectsUndersized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subuid")

	if err := os.WriteFile(path, []byte("srm-a:100000:1000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureSubIDFile(path, "srm-a"); err == nil || !strings.Contains(err.Error(), "too small") {
		t.Fatalf("undersized range must be rejected, got %v", err)
	}

	if err := os.WriteFile(path, []byte("srm-a:100000:65536\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureSubIDFile(path, "srm-a"); err != nil {
		t.Fatalf("adequate range must be accepted idempotently: %v", err)
	}
}

// TestEphemeralJobEnv guards the fix for ephemeral jobs running with HOME unset and
// USER=root (setpriv is not a login): HOME/USER/LOGNAME must be set, HOME must be the
// per-job dir (not the shared passwd HOME), and any extra (DinD DOCKER_HOST) appended.
func TestEphemeralJobEnv(t *testing.T) {
	env := ephemeralJobEnv("srm-acme", "/opt/actions-runners/acme/.ephemeral/3/_home",
		[]string{"DOCKER_HOST=unix:///run/srm/dind/acme/3/docker.sock"})
	want := map[string]bool{
		"RUNNER_ALLOW_RUNASROOT=0":                            false,
		"HOME=/opt/actions-runners/acme/.ephemeral/3/_home":   false,
		"USER=srm-acme":                                       false,
		"LOGNAME=srm-acme":                                    false,
		"DOCKER_HOST=unix:///run/srm/dind/acme/3/docker.sock": false,
	}
	for _, e := range env {
		if _, ok := want[e]; ok {
			want[e] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("ephemeralJobEnv missing %q; got %v", k, env)
		}
	}
	// HOME must NOT be the shared passwd home (that would persist creds across jobs).
	for _, e := range env {
		if e == "HOME=/opt/actions-runners/acme" {
			t.Error("HOME must be the per-job dir, not the shared passwd HOME")
		}
	}
	// No extra → still sets the core three + the guard, in a stable order.
	base := ephemeralJobEnv("srm-x", "/h", nil)
	if len(base) != 4 || base[0] != "RUNNER_ALLOW_RUNASROOT=0" || base[1] != "HOME=/h" {
		t.Errorf("base env shape wrong: %v", base)
	}
}

// TestDinDPaths pins the per-slot rootless paths so the daemon launcher, the env
// injection, and the per-job wipe all agree on one layout.
func TestDinDPaths(t *testing.T) {
	root := "/opt/actions-runners"
	u := &ubuntu{installRoot: root, opts: Options{User: "srm-acme", Org: "acme", Isolated: true}}
	// Paths are built with filepath.Join (Linux separators on the target host); assert
	// against the same join so the test is OS-agnostic on a dev box.
	if got, want := u.dindRuntimeDir("acme", "3"), filepath.Join(dindRunRoot, "acme", "3"); got != want {
		t.Errorf("dindRuntimeDir = %q, want %q", got, want)
	}
	if got, want := u.dindDataRoot("acme", "3"), filepath.Join(root, "acme", ".ephemeral", "3", ".docker-data"); got != want {
		t.Errorf("dindDataRoot = %q, want %q", got, want)
	}
	// DOCKER_CONFIG dir must live UNDER the per-job _home so the cycle reset wipes it and
	// the builder seed (writes here) and the job (reads here) share ONE buildx store -
	// the fix for BUILDX_BUILDER resolving against an empty _home/.docker.
	jobHome := filepath.Join(root, "acme", ".ephemeral", "3", "_home")
	if got, want := u.dindDockerConfigDir("acme", "3"), filepath.Join(jobHome, ".docker"); got != want {
		t.Errorf("dindDockerConfigDir = %q, want %q", got, want)
	}
	if !strings.HasPrefix(u.dindDockerConfigDir("acme", "3"), jobHome) {
		t.Errorf("dindDockerConfigDir must be under the per-job _home %q (so the reset wipes it)", jobHome)
	}
	if got, want := u.userHome("acme"), filepath.Join(root, "acme"); got != want {
		t.Errorf("isolated userHome = %q, want %q", got, want)
	}
	// Single-user mode: HOME is the install root, not an org subtree.
	u.opts.Isolated = false
	if got, want := u.userHome("acme"), root; got != want {
		t.Errorf("legacy userHome = %q, want %q", got, want)
	}
}

// TestDinDImageCache pins the persistent BuildKit image cache: it must live on the
// build-tool CacheRoot (which survives the per-cycle data-root wipe), NOT in the slot
// tree, and its filename must be keyed by the (sanitized) image ref so a changed
// BuildkitImage re-seeds rather than loading a stale image.
func TestDinDImageCache(t *testing.T) {
	if got, want := sanitizeImageRef("moby/buildkit:buildx-stable-1"), "moby_buildkit_buildx-stable-1"; got != want {
		t.Errorf("sanitizeImageRef = %q, want %q", got, want)
	}
	u := &ubuntu{installRoot: "/opt/actions-runners", opts: Options{
		User: "srm-acme", Org: "acme", Isolated: true,
		CacheRoot: "/opt/srm-cache/acme", BuildkitImage: "moby/buildkit:buildx-stable-1",
	}}
	got := u.dindImageCache()
	want := filepath.Join("/opt/srm-cache/acme", "srm-dind", "buildkit-moby_buildkit_buildx-stable-1.tar")
	if got != want {
		t.Errorf("dindImageCache = %q, want %q", got, want)
	}
	// Must NOT be under the slot tree, or the per-cycle wipe would defeat the cache.
	if strings.HasPrefix(got, u.ephemeralSlotDir("acme", "3")) {
		t.Errorf("image cache %q must not live under the wiped slot tree", got)
	}
	// A different image ref keys a different tar (no stale-image reuse).
	u.opts.BuildkitImage = "moby/buildkit:v0.99"
	if u.dindImageCache() == got {
		t.Error("a changed BuildkitImage must key a different cache tar")
	}
}
