//go:build windows

package service

import "context"

// DinDPortRanges is a no-op on Windows: rootless Docker-in-Docker is Linux-only.
func (m *Manager) DinDPortRanges(context.Context, []string) DinDPortReport { return DinDPortReport{} }
