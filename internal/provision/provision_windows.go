//go:build windows

// provision_windows.go is the Windows host reconciler - the analog of the Linux apt
// provisioner in provision_linux.go. The package-manager layer uses winget (the
// built-in App Installer, preferred) or Chocolatey (choco) as a fallback - the Windows
// counterpart of apt. The manifest's package list (DependencyManifest.AptPackages, the
// shared core field) therefore holds PACKAGE-MANAGER IDs on Windows: winget package ids
// (e.g. "Git.Git", "OpenJS.NodeJS.LTS") or choco ids (e.g. "git", "nodejs-lts"),
// matching whichever manager the host has. Setup scripts run via PowerShell instead of
// bash; tool-cache seeds use the Windows runner-image layout (node-vX-win-x64.zip / go
// windows-amd64.zip). It is plain Go (shells out to winget/choco/powershell/icacls), so
// it cross-compiles on linux for CI and only runs on Windows.
package provision

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/runner"
)

// Reconciler applies and inspects the host dependency layer.
type Reconciler interface {
	// Apply installs everything in the manifest that is missing (idempotent).
	Apply(ctx context.Context, m core.DependencyManifest) error
	// Drift reports manifest items not present on the host.
	Drift(ctx context.Context, m core.DependencyManifest) ([]string, error)
	// Seed pre-populates ONLY the tool cache (no packages/scripts), used to warm a
	// per-org cache after the host-global package/script step has already run.
	Seed(ctx context.Context, seeds []string) error
}

type windows struct {
	runnerUser    string // service account granted access to PersistentCachePaths + tool-cache seeds
	toolCacheRoot string // where tool-cache seeds land (AGENT_TOOLSDIRECTORY / RUNNER_TOOL_CACHE)
}

// NewWindows returns the Windows reconciler seeding the shared tool cache. Its
// methods must run elevated (installing machine-wide packages, granting ACLs).
func NewWindows(runnerUser string) Reconciler {
	return &windows{runnerUser: runnerUser, toolCacheRoot: config.DefaultToolCacheRoot}
}

// NewWindowsFor returns a reconciler that seeds into a specific tool-cache root
// owned by runnerUser. An empty root falls back to the shared default.
func NewWindowsFor(runnerUser, toolCacheRoot string) Reconciler {
	if toolCacheRoot == "" {
		toolCacheRoot = config.DefaultToolCacheRoot
	}
	return &windows{runnerUser: runnerUser, toolCacheRoot: toolCacheRoot}
}

// New returns the host reconciler for THIS OS (Windows: the winget/choco reconciler).
// It is the OS-agnostic entry point the service layer calls; the Linux build's
// provision_linux.go defines the matching New returning the apt reconciler.
func New(runnerUser string) Reconciler { return NewWindows(runnerUser) }

// NewFor is New scoped to a specific tool-cache root (e.g. an org's private root).
func NewFor(runnerUser, toolCacheRoot string) Reconciler {
	return NewWindowsFor(runnerUser, toolCacheRoot)
}

// Apply installs the manifest's packages via the host package manager (idempotently),
// creates persistent cache dirs granted to the runner user, runs setup scripts (the
// bring-your-own escape hatch), and seeds the tool cache. Pointing the runner at the
// seeded cache is the runner layer's job: each runner carries AGENT_TOOLSDIRECTORY in
// its per-service Environment (set at create/refresh), the Windows analog of the Linux
// systemd drop-in - so seeds become visible to runners created or refreshed afterward.
func (w *windows) Apply(ctx context.Context, m core.DependencyManifest) error {
	if len(m.AptPackages) > 0 {
		pm, err := detectPkgMgr(ctx)
		if err != nil {
			return err
		}
		for _, id := range m.AptPackages {
			if err := pm.ensure(ctx, id); err != nil {
				return fmt.Errorf("install %s via %s: %w", id, pm.name, err)
			}
		}
	}

	for _, dir := range m.PersistentCachePaths {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("cache path %s: %w", dir, err)
		}
		if err := w.grant(ctx, dir); err != nil {
			return fmt.Errorf("grant cache path %s: %w", dir, err)
		}
	}

	for _, script := range m.SetupScripts {
		if _, err := os.Stat(script); err != nil {
			return fmt.Errorf("setup script %s: %w", script, err)
		}
		// PowerShell is the Windows escape hatch (the analog of `bash script`).
		// -ExecutionPolicy Bypass so an unsigned operator script runs; the operator
		// authored it, so this is not a new trust boundary.
		if err := run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive",
			"-ExecutionPolicy", "Bypass", "-File", script); err != nil {
			return fmt.Errorf("setup script %s: %w", script, err)
		}
	}

	if err := w.Seed(ctx, m.ToolCacheSeeds); err != nil {
		return err
	}
	return nil
}

// Seed pre-populates the tool cache for each entry (no packages/scripts). It is the
// per-org warming step under isolation; on single-user Windows it warms the shared cache.
func (w *windows) Seed(ctx context.Context, seeds []string) error {
	for _, seed := range seeds {
		if err := w.seed(ctx, seed); err != nil {
			return fmt.Errorf("toolCacheSeed %q: %w", seed, err)
		}
	}
	return nil
}

// toolCacheName maps a seed tool to its actions/tool-cache directory name (what the
// matching setup-* action looks up; note the capital P for Python). Seeding under any
// other name yields a cache miss.
func toolCacheName(tool string) (string, bool) {
	switch tool {
	case "node":
		return "node", true
	case "go":
		return "go", true
	case "python":
		return "Python", true
	}
	return "", false
}

// seed pre-populates the host tool cache for one entry, e.g. "node@22.11.0" or
// "go@1.23.4". The layout matches actions/tool-cache: <root>/<tool>/<version>/x64
// plus an empty <root>/<tool>/<version>/x64.complete marker the setup-* action looks
// for. Idempotent per entry. Uses the Windows runner-image archives.
func (w *windows) seed(ctx context.Context, entry string) error {
	tool, version, ok := strings.Cut(entry, "@")
	if !ok || tool == "" || version == "" {
		return fmt.Errorf("expected tool@version (e.g. node@22.11.0, go@1.23.4)")
	}
	switch tool {
	case "node":
		// node-vX-win-x64.zip has a single top dir (node-vX-win-x64/) with node.exe
		// directly inside; stripping it lands node.exe under x64/.
		return w.seedZip(ctx, "node", version,
			fmt.Sprintf("https://nodejs.org/dist/v%s/node-v%s-win-x64.zip", version, version))
	case "go":
		// goX.windows-amd64.zip has a single top dir (go/) with bin/go.exe; stripping
		// it lands bin/go.exe under x64/.
		return w.seedZip(ctx, "go", version,
			fmt.Sprintf("https://go.dev/dl/go%s.windows-amd64.zip", version))
	case "python":
		// actions/python-versions ships Windows CPython as a nuget-style package whose
		// install is a setup.ps1 with a different layout than the linux tarball path; it
		// is not yet wired up here. Fail loudly rather than seed an unusable cache.
		return fmt.Errorf("python tool-cache seeding is not yet supported on Windows - install Python via --packages (winget/choco) or let actions/setup-python fetch it at job time")
	default:
		return fmt.Errorf("unsupported seed tool %q (supported on Windows: node, go)", tool)
	}
}

// seedZip pre-populates <root>/<cacheName>/<version>/x64 from a single upstream zip
// whose one top-level directory is stripped so the binaries land directly under x64.
// Writes the x64.complete marker and grants the runner user access. Idempotent.
func (w *windows) seedZip(ctx context.Context, cacheName, version, url string) error {
	base := filepath.Join(w.toolCacheRoot, cacheName, version, "x64")
	marker := base + ".complete"
	if fileExists(marker) {
		return nil
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("srm-%s-%s.zip", cacheName, version))
	if err := download(ctx, url, tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := extractZipStrip1(tmp, base); err != nil {
		return err
	}
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		return err
	}
	return w.grant(ctx, filepath.Join(w.toolCacheRoot, cacheName, version))
}

// Drift reports declared packages not installed and setup scripts missing on disk.
// (Setup-script side effects can't be inspected generically.) Tool-cache seeds are
// reported missing when their x64.complete marker is absent.
func (w *windows) Drift(ctx context.Context, m core.DependencyManifest) ([]string, error) {
	var missing []string
	if len(m.AptPackages) > 0 {
		pm, err := detectPkgMgr(ctx)
		if err != nil {
			return nil, err
		}
		for _, id := range m.AptPackages {
			if !pm.present(ctx, id) {
				missing = append(missing, pm.name+":"+id)
			}
		}
	}
	for _, script := range m.SetupScripts {
		if _, err := os.Stat(script); err != nil {
			missing = append(missing, "script:"+script)
		}
	}
	for _, seed := range m.ToolCacheSeeds {
		tool, version, ok := strings.Cut(seed, "@")
		if !ok {
			continue
		}
		name, known := toolCacheName(tool)
		if !known {
			continue
		}
		if !fileExists(filepath.Join(w.toolCacheRoot, name, version, "x64.complete")) {
			missing = append(missing, "toolcache:"+seed)
		}
	}
	return missing, nil
}

// grant gives the runner service account Modify on dir (inheritable) via icacls, so
// the low-privilege account can read+write the cache. The grantee is resolved through
// runner.IcaclsGrantee so the built-in service accounts go in by well-known SID
// (their localized names fail to resolve with error 1332). No-op when no user is set.
func (w *windows) grant(ctx context.Context, dir string) error {
	if w.runnerUser == "" {
		return nil
	}
	grant := fmt.Sprintf("%s:(OI)(CI)M", runner.IcaclsGrantee(w.runnerUser))
	return run(ctx, "icacls", dir, "/grant", grant, "/T", "/C", "/Q")
}

// pkgMgr is the detected host package manager (winget or choco).
type pkgMgr struct{ name string }

// detectPkgMgr picks winget (the built-in App Installer) over choco. Errors if
// neither is on PATH - the operator must install one (or use SetupScripts).
func detectPkgMgr(_ context.Context) (pkgMgr, error) {
	if _, err := exec.LookPath("winget"); err == nil {
		return pkgMgr{name: "winget"}, nil
	}
	if _, err := exec.LookPath("choco"); err == nil {
		return pkgMgr{name: "choco"}, nil
	}
	return pkgMgr{}, fmt.Errorf("no supported package manager on PATH - install winget (App Installer) or Chocolatey, or use host.setupScripts")
}

// ensure installs id if not already present. winget's install exit codes for an
// already-installed package are inconsistent across versions, so a winget install is
// gated on a presence check; choco install is idempotent on its own (it skips an
// installed package without --force), so it is simply invoked.
func (p pkgMgr) ensure(ctx context.Context, id string) error {
	if p.name == "winget" && p.present(ctx, id) {
		return nil
	}
	return p.install(ctx, id)
}

func (p pkgMgr) install(ctx context.Context, id string) error {
	switch p.name {
	case "winget":
		return run(ctx, "winget", "install", "--id", id, "--exact", "--silent",
			"--accept-source-agreements", "--accept-package-agreements", "--disable-interactivity")
	case "choco":
		return run(ctx, "choco", "install", id, "-y", "--no-progress")
	}
	return fmt.Errorf("unknown package manager %q", p.name)
}

// present reports whether id is installed. For winget, `list --id --exact` exits 0
// only when the package is installed. For choco, the local list (machine-readable
// "id|version") is scanned; --local-only is passed for choco 1.x and ignored/implied
// by choco 2.x, so the check works across both.
func (p pkgMgr) present(ctx context.Context, id string) bool {
	switch p.name {
	case "winget":
		return exec.CommandContext(ctx, "winget", "list", "--id", id, "--exact",
			"--accept-source-agreements", "--disable-interactivity").Run() == nil
	case "choco":
		out, _ := exec.CommandContext(ctx, "choco", "list", "--local-only", "--exact",
			"--limit-output", id).Output()
		return strings.Contains(string(out), id+"|")
	}
	return false
}

// extractZipStrip1 unzips src into dst, stripping the single top-level directory each
// entry carries (node-vX-win-x64/ or go/), so binaries land directly under dst.
// Zip-Slip guarded (a crafted entry escaping dst is rejected).
func extractZipStrip1(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	clean := filepath.Clean(dst)
	for _, f := range r.File {
		name := stripFirstComponent(f.Name)
		if name == "" {
			continue // the top-level dir itself
		}
		target := filepath.Join(dst, name)
		if target != clean && !strings.HasPrefix(target, clean+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe zip path: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := copyZipFile(f, target); err != nil {
			return err
		}
	}
	return nil
}

// copyZipFile writes one zip entry to disk preserving its mode.
func copyZipFile(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode()|0o200)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

// stripFirstComponent drops the leading path segment of a zip entry name (zip names
// use forward slashes regardless of OS). Returns "" for a single-segment name (the
// top-level directory entry itself), which the caller skips.
func stripFirstComponent(name string) string {
	name = strings.TrimPrefix(name, "/")
	i := strings.IndexByte(name, '/')
	if i < 0 {
		return ""
	}
	return name[i+1:]
}

func download(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
