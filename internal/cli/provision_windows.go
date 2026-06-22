//go:build windows

package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func newProvisionCmd() *cobra.Command {
	var packages, seed, seedNode string
	c := &cobra.Command{
		Use:   "provision",
		Short: "Apply the host dependency layer (winget/choco packages, tool cache, setup scripts) - run elevated on the target",
		Long: "Self-hosted runners ship bare, so the job toolchain lives on the host. " +
			"provision applies the config `host:` manifest plus any --packages/--seed flags, " +
			"idempotently. Packages install via winget (preferred) or Chocolatey, so " +
			"host.aptPackages / --packages take package-manager IDs (e.g. winget \"Git.Git\", " +
			"\"OpenJS.NodeJS.LTS\"; or choco \"git\", \"nodejs-lts\"). Tool-cache seeds use the " +
			"Windows runner-image archives (node, go). Provision the host before creating " +
			"runners so the toolchain is on PATH when they register.",
		RunE: func(_ *cobra.Command, _ []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()

			man := mgr.HostManifest()
			man.AptPackages = append(man.AptPackages, splitCSV(packages)...)
			man.ToolCacheSeeds = append(man.ToolCacheSeeds, splitCSV(seed)...)
			for _, v := range splitCSV(seedNode) {
				man.ToolCacheSeeds = append(man.ToolCacheSeeds, "node@"+v)
			}

			if man.Empty() {
				return fmt.Errorf("nothing to provision - set host.aptPackages in config or pass --packages/--seed")
			}

			ctx := context.Background()
			if missing, derr := mgr.HostDrift(ctx, man); derr == nil {
				if len(missing) == 0 {
					fmt.Println("packages/scripts already present - applying anyway (idempotent)")
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
	c.Flags().StringVar(&packages, "packages", "", "comma-separated package-manager IDs to install (winget or choco, e.g. Git.Git,OpenJS.NodeJS.LTS)")
	c.Flags().StringVar(&seed, "seed", "", "comma-separated tool@version seeds for the tool cache (e.g. node@22.11.0,go@1.23.4)")
	c.Flags().StringVar(&seedNode, "seed-node", "", "shorthand for --seed node@<ver> (comma-separated exact versions)")
	return c
}
