//go:build !linux

package runner

import (
	"context"
	"errors"
)

// ReadDinDNetns is Linux-only: rootless DinD lanes exist only on Linux hosts.
func ReadDinDNetns(context.Context, string, string) (DinDNetns, error) {
	return DinDNetns{}, errors.New("rootless DinD port ranges are only available on Linux")
}
