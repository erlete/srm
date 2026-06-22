//go:build !windows

package cli

// systemConfigPath is the canonical machine-wide config location on Linux:
// /etc/srm/config.yaml (root-owned). See the systemConfigPath doc in root.go.
const systemConfigPath = "/etc/srm/config.yaml"
