// Package provision applies a profile's host-once dependency manifest to an
// Ubuntu x64 host (apt packages, setup scripts, persistent cache paths),
// idempotently and with drift detection. The host is the durable substrate;
// runners consume it. Self-hosted runners ship bare - no Node/Python/Docker -
// so without this a job that "just works" on ubuntu-latest fails with 127.
package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
)

// Reconciler applies and inspects the host dependency layer.
type Reconciler interface {
	// Apply installs everything in the manifest that is missing (idempotent).
	Apply(ctx context.Context, m core.DependencyManifest) error
	// Drift reports manifest items not present on the host.
	Drift(ctx context.Context, m core.DependencyManifest) ([]string, error)
	// Seed pre-populates ONLY the tool cache (no apt/scripts), used to warm a
	// per-org cache after the host-global apt/scripts step has already run.
	Seed(ctx context.Context, seeds []string) error
}

type ubuntu struct {
	runnerUser    string // owns the tool-cache seeds + PersistentCachePaths
	toolCacheRoot string // where tool-cache seeds land (per-org or the shared root)
}

// NewUbuntu returns the Ubuntu reconciler seeding the shared tool cache. Its
// methods must run as root.
func NewUbuntu(runnerUser string) Reconciler {
	return &ubuntu{runnerUser: runnerUser, toolCacheRoot: config.DefaultToolCacheRoot}
}

// NewUbuntuFor returns a reconciler that seeds into a specific tool-cache root
// (e.g. an org's private /opt/hostedtoolcache/<org>) owned by runnerUser. An
// empty root falls back to the shared default.
func NewUbuntuFor(runnerUser, toolCacheRoot string) Reconciler {
	if toolCacheRoot == "" {
		toolCacheRoot = config.DefaultToolCacheRoot
	}
	return &ubuntu{runnerUser: runnerUser, toolCacheRoot: toolCacheRoot}
}

// Apply installs apt packages (idempotently - apt is a no-op for present ones),
// creates persistent cache dirs owned by the runner user, runs setup scripts
// (the bring-your-own escape hatch for complex stacks), and seeds the shared
// tool cache (node/go/python) so setup-* actions hit a warm cache.
func (u *ubuntu) Apply(ctx context.Context, m core.DependencyManifest) error {
	if len(m.AptPackages) > 0 {
		env := append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		if err := runEnv(ctx, env, "apt-get", "-o", "DPkg::Lock::Timeout=180", "update"); err != nil {
			return fmt.Errorf("apt update: %w", err)
		}
		args := append([]string{"-y", "-o", "DPkg::Lock::Timeout=180", "install"}, m.AptPackages...)
		if err := runEnv(ctx, env, "apt-get", args...); err != nil {
			return fmt.Errorf("apt install %v: %w", m.AptPackages, err)
		}
	}

	for _, dir := range m.PersistentCachePaths {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return fmt.Errorf("cache path %s: %w", dir, err)
		}
		if u.runnerUser != "" {
			if err := run(ctx, "chown", "-R", u.runnerUser+":"+u.runnerUser, dir); err != nil {
				return fmt.Errorf("chown cache path %s: %w", dir, err)
			}
		}
	}

	for _, script := range m.SetupScripts {
		if _, err := os.Stat(script); err != nil {
			return fmt.Errorf("setup script %s: %w", script, err)
		}
		if err := run(ctx, "bash", script); err != nil {
			return fmt.Errorf("setup script %s: %w", script, err)
		}
	}

	return u.Seed(ctx, m.ToolCacheSeeds)
}

// Seed pre-populates the tool cache for each entry (no apt/scripts). It is the
// per-org warming step: with isolation on, Apply runs apt/scripts once and then
// Seed runs per org into each private tool cache.
func (u *ubuntu) Seed(ctx context.Context, seeds []string) error {
	for _, seed := range seeds {
		if err := u.seed(ctx, seed); err != nil {
			return fmt.Errorf("toolCacheSeed %q: %w", seed, err)
		}
	}
	return nil
}

// toolCacheName maps a seed tool to its actions/tool-cache directory name. The
// names are what the matching setup-* action looks up (note the capital P for
// Python); seeding under any other name yields a cache miss.
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

// seed pre-populates the host tool cache for one entry, e.g. "node@22.11.0",
// "go@1.23.4", or "python@3.12.7". Layout matches actions/tool-cache:
// <root>/<tool>/<version>/x64 plus an empty <root>/<tool>/<version>/x64.complete
// marker that the setup-* action looks for. Idempotent per entry.
func (u *ubuntu) seed(ctx context.Context, entry string) error {
	tool, version, ok := strings.Cut(entry, "@")
	if !ok || tool == "" || version == "" {
		return fmt.Errorf("expected tool@version (e.g. node@22.11.0, go@1.23.4, python@3.12.7)")
	}
	switch tool {
	case "node":
		// strip the leading node-vX-linux-x64/ component so base/bin/node exists.
		return u.seedTarball(ctx, "node", version,
			fmt.Sprintf("https://nodejs.org/dist/v%s/node-v%s-linux-x64.tar.gz", version, version))
	case "go":
		// the go.dev tarball's top-level go/ is stripped so base/bin/go exists.
		return u.seedTarball(ctx, "go", version,
			fmt.Sprintf("https://go.dev/dl/go%s.linux-amd64.tar.gz", version))
	case "python":
		return u.seedPython(ctx, version)
	default:
		return fmt.Errorf("unsupported seed tool %q (supported: node, go, python)", tool)
	}
}

// seedTarball pre-populates <root>/<cacheName>/<version>/x64 from a single
// upstream tarball whose one top-level directory is stripped so the binaries
// land directly under x64. Writes the x64.complete marker. Idempotent.
func (u *ubuntu) seedTarball(ctx context.Context, cacheName, version, url string) error {
	base := filepath.Join(u.toolCacheRoot, cacheName, version, "x64")
	marker := base + ".complete"
	if fileExists(marker) {
		return nil
	}
	if err := os.MkdirAll(base, 0o775); err != nil {
		return err
	}
	tarball := filepath.Join(os.TempDir(), "srm-"+cacheName+"-"+version+".tar.gz")
	if err := download(ctx, url, tarball); err != nil {
		return err
	}
	defer os.Remove(tarball)
	if err := run(ctx, "tar", "-xzf", tarball, "-C", base, "--strip-components=1"); err != nil {
		return err
	}
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		return err
	}
	if u.runnerUser != "" {
		return run(ctx, "chown", "-R", u.runnerUser+":"+u.runnerUser, filepath.Join(u.toolCacheRoot, cacheName, version))
	}
	return nil
}

// pythonManifestURL is actions/python-versions' static version manifest - the
// same source actions/setup-python resolves against. It's a raw file, so reading
// it avoids the authenticated-GitHub-API rate limit.
const pythonManifestURL = "https://raw.githubusercontent.com/actions/python-versions/main/versions-manifest.json"

type pyRelease struct {
	Version string `json:"version"`
	Files   []struct {
		Filename        string `json:"filename"`
		Arch            string `json:"arch"`
		Platform        string `json:"platform"`
		PlatformVersion string `json:"platform_version"`
		DownloadURL     string `json:"download_url"`
	} `json:"files"`
}

// seedPython pre-populates <root>/Python/<version>/x64. setup-python does NOT
// build from python.org - it pulls a per-Ubuntu prebuilt CPython from the
// actions/python-versions releases and runs that asset's setup.sh, which copies
// into $AGENT_TOOLSDIRECTORY/Python/<ver>/x64 and writes the x64.complete marker.
// We do exactly that, so the layout/marker are identical to a real cache hit.
func (u *ubuntu) seedPython(ctx context.Context, version string) error {
	root := u.toolCacheRoot
	if fileExists(filepath.Join(root, "Python", version, "x64.complete")) {
		return nil
	}
	osVer, err := osReleaseVersionID()
	if err != nil {
		return err
	}
	url, err := resolvePythonAsset(ctx, version, osVer)
	if err != nil {
		return err
	}
	tarball := filepath.Join(os.TempDir(), "srm-python-"+version+".tar.gz")
	if err := download(ctx, url, tarball); err != nil {
		return err
	}
	defer os.Remove(tarball)
	extract := filepath.Join(os.TempDir(), "srm-python-"+version)
	_ = os.RemoveAll(extract)
	if err := os.MkdirAll(extract, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(extract)
	if err := run(ctx, "tar", "-xzf", tarball, "-C", extract); err != nil {
		return err
	}
	if !fileExists(filepath.Join(extract, "setup.sh")) {
		return fmt.Errorf("python %s asset missing setup.sh", version)
	}
	// setup.sh copies ./* into $AGENT_TOOLSDIRECTORY/Python/<ver>/x64 and writes the
	// x64.complete marker - it uses paths relative to its CWD, so it must run with
	// the extracted directory as the working directory.
	cmd := exec.CommandContext(ctx, "bash", "setup.sh")
	cmd.Dir = extract
	cmd.Env = append(os.Environ(), "AGENT_TOOLSDIRECTORY="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("python setup.sh: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if u.runnerUser != "" {
		return run(ctx, "chown", "-R", u.runnerUser+":"+u.runnerUser, filepath.Join(root, "Python", version))
	}
	return nil
}

// resolvePythonAsset finds the linux/x64 download URL for an exact Python
// version built for the host's Ubuntu release, from the python-versions manifest.
func resolvePythonAsset(ctx context.Context, version, osVer string) (string, error) {
	data, err := httpGetBytes(ctx, pythonManifestURL)
	if err != nil {
		return "", err
	}
	var releases []pyRelease
	if err := json.Unmarshal(data, &releases); err != nil {
		return "", fmt.Errorf("parse python manifest: %w", err)
	}
	for _, r := range releases {
		if r.Version != version {
			continue
		}
		for _, f := range r.Files {
			if f.Platform == "linux" && f.Arch == "x64" && f.PlatformVersion == osVer {
				return f.DownloadURL, nil
			}
		}
		return "", fmt.Errorf("python %s has no linux/x64 build for Ubuntu %s", version, osVer)
	}
	return "", fmt.Errorf("python %s not found in actions/python-versions manifest", version)
}

// osReleaseVersionID returns the Ubuntu release id (e.g. "22.04") from
// /etc/os-release, used to pick the matching prebuilt Python asset.
func osReleaseVersionID() (string, error) {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "VERSION_ID="); ok {
			return strings.Trim(v, `"`), nil
		}
	}
	return "", fmt.Errorf("VERSION_ID not found in /etc/os-release")
}

func httpGetBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
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

// Drift reports declared apt packages not installed and setup scripts missing
// on disk. (Setup-script side effects can't be inspected generically.)
func (u *ubuntu) Drift(ctx context.Context, m core.DependencyManifest) ([]string, error) {
	var missing []string
	for _, pkg := range m.AptPackages {
		if exec.CommandContext(ctx, "dpkg", "-s", pkg).Run() != nil {
			missing = append(missing, "apt:"+pkg)
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
		if !fileExists(filepath.Join(u.toolCacheRoot, name, version, "x64.complete")) {
			missing = append(missing, "toolcache:"+seed)
		}
	}
	return missing, nil
}

func run(ctx context.Context, name string, args ...string) error {
	return runEnv(ctx, nil, name, args...)
}

func runEnv(ctx context.Context, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = env
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
