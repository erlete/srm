//go:build !linux

package runner

import (
	"context"
	"errors"
	"os/user"
)

var errDinDLinuxOnly = errors.New("rootless DinD port ranges are only available on Linux")

func installDinDPortWrapperImpl(string) error { return errDinDLinuxOnly }

func probeDinDPortWrapperImpl(context.Context, *user.User, string) error { return errDinDLinuxOnly }

// DinDPortWrapperProblem is Linux-only (rootless DinD lanes exist only on Linux).
func DinDPortWrapperProblem() string { return "" }

// processExited is never consulted off Linux (the rootless lane is Linux-only).
func processExited(int) bool { return false }

// ReadDinDNetns is Linux-only: rootless DinD lanes exist only on Linux hosts.
func ReadDinDNetns(context.Context, string, string) (DinDNetns, error) {
	return DinDNetns{}, errDinDLinuxOnly
}
