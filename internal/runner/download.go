// Package runner orchestrates the on-machine actions/runner agent. On Linux it uses
// systemd + apt (see orchestrator.go); on Windows the Service Control Manager +
// config.cmd/run.cmd (see windows.go). The download/dotenv/unit-name helpers are
// pure and OS-agnostic; the os/arch-specific archive name lives in
// download_{linux,windows}.go.
package runner

import (
	"fmt"
	"path"
	"strconv"
	"strings"
)

const releaseBase = "https://github.com/actions/runner/releases/download"

// assetPrefix / assetSuffix bracket the version inside the os/arch archive name
// (actions-runner-<os>-x64-<version>.<ext>) and are defined per OS in
// download_linux.go / download_windows.go. VersionFromURL is the inverse of
// AssetName, so they must stay in lockstep - TestVersionFromURL round-trips them.

// DownloadURL returns the actions/runner archive URL for this OS's x64 build at the
// given version (without a leading "v"). The os/arch is fixed per build.
func DownloadURL(version string) string {
	return fmt.Sprintf("%s/v%s/%s", releaseBase, version, AssetName(version))
}

// AssetName is the archive filename for a runner version (os/arch fixed per build).
func AssetName(version string) string {
	return assetPrefix + version + assetSuffix
}

// VersionFromURL extracts the agent version (e.g. "2.335.1") from a runner
// download URL or bare asset name by peeling AssetName's prefix/suffix off the
// final path segment. It returns "" when the name doesn't match this build's
// os/arch asset shape (an unrecognised URL, or a different os/arch), so callers
// treat the version as unknown rather than recording a bogus one. Query/fragment
// suffixes are tolerated. This is the create-time source of the agentVersion
// recorded in the state manifest.
func VersionFromURL(url string) string {
	base := path.Base(url)
	if i := strings.IndexAny(base, "?#"); i >= 0 {
		base = base[:i]
	}
	if !strings.HasPrefix(base, assetPrefix) || !strings.HasSuffix(base, assetSuffix) {
		return ""
	}
	return base[len(assetPrefix) : len(base)-len(assetSuffix)]
}

// CompareVersions orders two actions/runner version strings ("2.335.1") field by
// numeric field, returning -1 if a < b, 0 if equal, +1 if a > b. A missing
// trailing field counts as 0, so "2.335" < "2.335.1". It is used by the upgrade
// engine's anti-downgrade guard; the guard only fires on a confident a<b, so any
// version it can't parse cleanly (a non-numeric field) sorts as 0 (equal) rather
// than risking a wrong refusal - the engine treats "can't tell" as "allow", and
// the operator can still gate with --force.
func CompareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		ai, bi := versionField(as, i), versionField(bs, i)
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	return 0
}

// versionField parses the i-th dotted field as an int, treating a missing or
// non-numeric field as 0.
func versionField(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, err := strconv.Atoi(parts[i])
	if err != nil {
		return 0
	}
	return n
}
