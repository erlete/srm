//go:build !windows

package runner

import "context"

// EnsureSecretsReadable is a no-op off Windows: the Linux ephemeral cycle runs as
// root (the slot unit's ExecStart), which already reads the root-only secrets.age +
// secrets.pass directly, so no per-account grant is needed. The signature matches the
// Windows build so callers stay platform-agnostic.
func EnsureSecretsReadable(_ context.Context, _ string, _ ...string) error { return nil }
