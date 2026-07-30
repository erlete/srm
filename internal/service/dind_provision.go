//go:build !windows

package service

import (
	"context"
	"os"

	"github.com/erlete/srm/internal/core"
)

// rootlessDinDAptDeps are the Ubuntu-universe packages rootless Docker needs, applied
// by the reconciler's apt step (before the setup script runs). uidmap brings the
// setuid newuidmap/newgidmap (the classic "starts but cannot map uids" trap that
// silently queues the whole fleet); fuse-overlayfs + slirp4netns are the rootless
// storage/network drivers; dbus-user-session backs the rootless systemd user session;
// ca-certificates + curl let the setup script fetch Docker's apt keyring.
var rootlessDinDAptDeps = []string{
	"uidmap", "fuse-overlayfs", "slirp4netns", "dbus-user-session", "ca-certificates", "curl",
}

// rootlessDinDSetupScript adds Docker's official apt repo (once), installs Docker CE +
// the rootless extras (dockerd-rootless.sh, rootlesskit), enables unprivileged user
// namespaces (which rootless dockerd clones), and loads the fuse module. Idempotent:
// the repo/keyring are only added when dockerd is absent, apt-get install is a no-op
// for present packages, and the sysctl file is rewritten in place. It runs as root
// (provision does), mirroring the generated node/corepack scripts.
const rootlessDinDSetupScript = `#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# Docker CE + rootless extras via the official apt repo (added once).
if ! command -v dockerd >/dev/null 2>&1; then
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  . /etc/os-release
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${VERSION_CODENAME} stable" > /etc/apt/sources.list.d/docker.list
  apt-get -o DPkg::Lock::Timeout=180 update
fi
apt-get -y -o DPkg::Lock::Timeout=180 install docker-ce docker-ce-cli containerd.io docker-ce-rootless-extras

# Unprivileged user namespaces (rootless dockerd clones one); persisted + applied now.
cat > /etc/sysctl.d/99-srm-rootless-dind.conf <<'SYSCTL'
kernel.unprivileged_userns_clone=1
user.max_user_namespaces=28633
SYSCTL
sysctl --system >/dev/null 2>&1 || true

# fuse module for the fuse-overlayfs storage driver.
modprobe fuse 2>/dev/null || true

echo "rootless-DinD prerequisites installed"
`

// ProvisionRootlessDinD installs THIS host's rootless-Docker prerequisites (the
// universe rootless deps, Docker CE + rootless extras, the userns sysctls, the fuse
// module), idempotently. It is the one-command remediation for a failed DinDReadiness
// probe (FP1: a missing prereq otherwise silently queues every DinD job). It reuses
// the provision reconciler (apt update/install + script execution) via a short-lived
// generated setup script, so it must run as root. Synchronous: the temp script lives
// only for the duration of the apply.
func (m *Manager) ProvisionRootlessDinD(ctx context.Context) error {
	f, err := os.CreateTemp("", "srm-rootless-dind-*.sh")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(rootlessDinDSetupScript); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	man := core.DependencyManifest{
		AptPackages:  rootlessDinDAptDeps,
		SetupScripts: []string{f.Name()},
	}
	return m.ProvisionHost(ctx, man)
}
