//go:build !windows

package service

// StatePath is the host state manifest location on Linux: /var/lib/srm/state.json
// (root 0600), alongside the ephemeral control dir. See the StatePath doc in state.go.
const StatePath = "/var/lib/srm/state.json"
