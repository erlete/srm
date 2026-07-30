//go:build windows

package service

import (
	"context"
	"fmt"
)

// ProvisionRootlessDinD is Linux-only: rootless Docker (rootlesskit, user namespaces,
// fuse-overlayfs) has no Windows analog. The Windows ephemeral lane uses a wiped
// profile per job instead. Returns an error so a caller never silently no-ops.
func (m *Manager) ProvisionRootlessDinD(context.Context) error {
	return fmt.Errorf("rootless DinD is Linux-only (not applicable on Windows)")
}
