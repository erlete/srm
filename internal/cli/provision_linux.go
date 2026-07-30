//go:build !windows

package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// nodeRuntimeLibs are the shared libraries the Node tarballs that
// actions/setup-node downloads link against. NodeSource's own package pulls in
// what /it/ needs, but setup-node's prebuilt Node fails at runtime
// ("libatomic.so.1: cannot open shared object file") unless these are present.
// ca-certificates/curl are needed by the NodeSource installer itself.
var nodeRuntimeLibs = []string{"libatomic1", "libstdc++6", "ca-certificates", "curl"}

// Generated setup scripts for the convenience flags. They run as root on the
// host. NodeSource installs Node into /usr/bin (a system path - on every
// runner's captured PATH and unaffected by the service ProtectHome sandbox).
const nodeSourceScript = `#!/usr/bin/env bash
set -euo pipefail
curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
apt-get install -y nodejs
node --version
`

const corepackScript = `#!/usr/bin/env bash
set -euo pipefail
corepack enable
corepack prepare pnpm@latest --activate || true
pnpm --version || true
`

func newProvisionCmd() *cobra.Command {
	var apt, seed, seedNode string
	var node, corepack, rootlessDinD bool
	c := &cobra.Command{
		Use:   "provision",
		Short: "Apply the host dependency layer (apt packages, Node, setup scripts) - run as root on the target",
		Long: "Self-hosted runners ship bare, so the job toolchain lives on the host. " +
			"provision applies the config `host:` manifest plus any --apt/--node/--corepack " +
			"flags, idempotently. Provision the host before creating runners so the toolchain " +
			"is on PATH when they register.",
		RunE: func(_ *cobra.Command, _ []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()

			man := mgr.HostManifest()
			man.AptPackages = append(man.AptPackages, splitCSV(apt)...)

			var tmp []string
			defer func() {
				for _, p := range tmp {
					_ = os.Remove(p)
				}
			}()
			if node {
				// apt runs before setup scripts, so the runtime libs are present
				// when NodeSource (and later, setup-node's Node) needs them.
				man.AptPackages = append(man.AptPackages, nodeRuntimeLibs...)
				p, err := writeTempScript("srm-node", nodeSourceScript)
				if err != nil {
					return err
				}
				tmp = append(tmp, p)
				man.SetupScripts = append(man.SetupScripts, p)
			}
			if corepack {
				p, err := writeTempScript("srm-corepack", corepackScript)
				if err != nil {
					return err
				}
				tmp = append(tmp, p)
				man.SetupScripts = append(man.SetupScripts, p)
			}
			man.ToolCacheSeeds = append(man.ToolCacheSeeds, splitCSV(seed)...)
			for _, v := range splitCSV(seedNode) {
				man.ToolCacheSeeds = append(man.ToolCacheSeeds, "node@"+v)
			}

			ctx := context.Background()

			// Rootless-DinD prerequisites (FP1): the one-command install of Docker CE +
			// rootless extras, uidmap, fuse-overlayfs, slirp4netns, and the userns
			// sysctls. Idempotent. Re-probe readiness afterwards so the fix is visible.
			if rootlessDinD {
				fmt.Println("installing rootless-DinD prerequisites (docker-ce + rootless-extras, uidmap, fuse-overlayfs, slirp4netns, userns sysctls)…")
				if err := mgr.ProvisionRootlessDinD(ctx); err != nil {
					return fmt.Errorf("rootless-DinD prereqs: %w", err)
				}
				fmt.Println("rootless-DinD prerequisites applied")
				printDinDReadiness(mgr.DinDReadiness(mgr.OrgNames()))
				if !mgr.Config().Docker.RootlessDinD {
					fmt.Println("note: set docker.rootlessDinD: true in config to enable the per-job rootless daemon on ephemeral lanes.")
				}
			}

			if man.Empty() {
				if rootlessDinD {
					return nil // DinD prereqs handled above; nothing else to provision
				}
				return fmt.Errorf("nothing to provision - set host.aptPackages in config or pass --apt/--node/--corepack/--rootless-dind")
			}

			if missing, derr := mgr.HostDrift(ctx, man); derr == nil {
				if len(missing) == 0 {
					fmt.Println("apt/scripts already present - applying anyway (idempotent)")
				} else {
					fmt.Printf("missing, will install: %v\n", missing)
				}
			}

			if err := mgr.ProvisionHost(ctx, man); err != nil {
				return err
			}
			fmt.Println("host provisioned")
			return nil
		},
	}
	c.Flags().StringVar(&apt, "apt", "", "comma-separated apt packages to install")
	c.Flags().BoolVar(&node, "node", false, "install Node.js (NodeSource 22.x) into /usr/bin")
	c.Flags().BoolVar(&corepack, "corepack", false, "enable corepack (pnpm/yarn shims)")
	c.Flags().BoolVar(&rootlessDinD, "rootless-dind", false, "install the rootless-Docker prerequisites (docker-ce + rootless-extras, uidmap, fuse-overlayfs, slirp4netns, userns sysctls)")
	c.Flags().StringVar(&seed, "seed", "", "comma-separated tool@version seeds for the shared tool cache (e.g. node@22.11.0,go@1.23.4,python@3.12.7)")
	c.Flags().StringVar(&seedNode, "seed-node", "", "shorthand for --seed node@<ver> (comma-separated exact versions)")
	return c
}

func writeTempScript(prefix, content string) (string, error) {
	f, err := os.CreateTemp("", prefix+"-*.sh")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}
