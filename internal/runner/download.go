// Package runner orchestrates the on-machine actions/runner agent on Ubuntu
// x64 hosts (systemd + apt). The download/dotenv/unit-name helpers are pure and
// implemented now; the agent install/register/supervise/remove flow is stubbed
// for v1 (see orchestrator.go).
package runner

import "fmt"

const releaseBase = "https://github.com/actions/runner/releases/download"

// DownloadURL returns the actions/runner tarball URL for linux-x64 at the given
// version (without a leading "v"). The tool targets Ubuntu x64 only, so the
// os/arch is fixed.
func DownloadURL(version string) string {
	return fmt.Sprintf("%s/v%s/%s", releaseBase, version, AssetName(version))
}

// AssetName is the tarball filename for a runner version.
func AssetName(version string) string {
	return fmt.Sprintf("actions-runner-linux-x64-%s.tar.gz", version)
}
