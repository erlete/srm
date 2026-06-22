//go:build windows

package runner

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// writeServiceEnvironment sets the per-service Environment value (REG_MULTI_SZ) on a
// Windows service so the Service Control Manager merges these NAME=VALUE entries onto the
// service's inherited environment when it STARTS that one service. It is the native,
// runner-scoped analog of the Linux systemd drop-in's Environment= lines: surgical to a
// single service (not host-global like setx), additive (it must NOT re-list PATH/TEMP),
// and read by the SCM only at start - so callers must (re)start the service after writing
// for the runner agent to see the change. The Windows runner service does NOT read a .env
// file, so this registry value is the only mechanism that reaches the agent process.
//
// The service key must already exist (config.cmd --runasservice for persistent runners,
// or mgr.CreateService for the ephemeral supervisor created it). Requires Administrator,
// which every write path that calls this already holds.
func writeServiceEnvironment(svcName string, env []string) error {
	path := `SYSTEM\CurrentControlSet\Services\` + svcName
	return setMultiStringValue(registry.LOCAL_MACHINE, path, "Environment", env)
}

// setMultiStringValue opens an existing registry key for write and sets a REG_MULTI_SZ
// value. Split out from writeServiceEnvironment (which hard-codes the HKLM service path)
// so the registry round-trip can be exercised against a writable test key without
// Administrator. Errors if the key does not already exist.
func setMultiStringValue(root registry.Key, path, name string, vals []string) error {
	k, err := registry.OpenKey(root, path, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open registry key %s: %w", path, err)
	}
	defer k.Close()
	if err := k.SetStringsValue(name, vals); err != nil {
		return fmt.Errorf("set %s on %s: %w", name, path, err)
	}
	return nil
}
