//go:build windows

package cli

import "github.com/erlete/srm/internal/config"

// systemConfigPath is the canonical machine-wide config location on Windows:
// %ProgramData%\srm\config.yaml (see config.SystemConfigPath), the analog of
// /etc/srm/config.yaml. See the systemConfigPath doc in root.go.
var systemConfigPath = config.SystemConfigPath()
