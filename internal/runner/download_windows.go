//go:build windows

package runner

// assetPrefix / assetSuffix bracket the version inside a Windows-x64 archive name
// (actions-runner-win-x64-<version>.zip). Symmetric with the Linux tree's linux-x64
// (download_linux.go). NOTE: the Windows asset is a .zip, not .tar.gz (see extractZip).
const (
	assetPrefix = "actions-runner-win-x64-"
	assetSuffix = ".zip"
)

// AssetOS is the OS string GitHub's runner-application API reports for this build's
// download (used by the service layer to pick the right asset). Linux: "linux".
const AssetOS = "win"
