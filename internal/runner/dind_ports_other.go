//go:build !linux

package runner

import (
	"context"
	"errors"
)

// ReadDinDPortRange is Linux-only: rootless DinD lanes exist only on Linux hosts.
func ReadDinDPortRange(context.Context, string, string) (int, int, error) {
	return 0, 0, errors.New("rootless DinD port ranges are only available on Linux")
}
