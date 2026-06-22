//go:build windows

package service

import "github.com/erlete/srm/internal/config"

// StatePath is the host state manifest location on Windows: %ProgramData%\srm\state.json
// (see config.StateManifestPath), the analog of /var/lib/srm/state.json. See the
// StatePath doc in state.go.
var StatePath = config.StateManifestPath()
