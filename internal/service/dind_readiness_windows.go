//go:build windows

package service

// DinDReadiness is a no-op on Windows: rootless Docker-in-Docker is a Linux-only
// feature with no Windows analog. The stub keeps the doctor / Health call sites
// OS-agnostic (Enabled=false means "not applicable").
func (m *Manager) DinDReadiness(_ []string) DinDReport { return DinDReport{} }
