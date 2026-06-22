//go:build windows

package cli

import "github.com/erlete/srm/internal/config"

// dindReadiness is a no-op on Windows: rootless Docker-in-Docker is a Linux-only
// feature (the Linux build's doctor_linux.go prints the host readiness probe). The
// stub keeps the doctor command's call site OS-agnostic.
func dindReadiness(_ *config.Config, _ []string) {}
