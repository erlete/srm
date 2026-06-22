//go:build !windows

package runner

// assetPrefix / assetSuffix bracket the version inside a linux-x64 tarball name
// (actions-runner-linux-x64-<version>.tar.gz). The Windows analog (win-x64 .zip)
// lives in download_windows.go.
const (
	assetPrefix = "actions-runner-linux-x64-"
	assetSuffix = ".tar.gz"
)

// AssetOS is the OS string GitHub's runner-application API reports for this build's
// download (used by the service layer to pick the right asset). Windows: "win".
const AssetOS = "linux"
