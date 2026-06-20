package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/erlete/srm/internal/service"
)

// newUninstallCmd tears down srm's footprint on this host. It previews the exact
// plan, then (unless --yes) requires the operator to type the hostname before
// executing. --org scopes the purge to one org and never touches shared infra.
func newUninstallCmd() *cobra.Command {
	var yes, force, keepConfig, keepBinary, keepGitHub, purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove srm's runners, lanes, users, and caches from this host (--purge also removes config)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()

			opts := service.UninstallOpts{
				Org:        flagOrg, // "" = full host purge
				Force:      force,
				KeepConfig: keepConfig,
				KeepBinary: keepBinary,
				KeepGitHub: keepGitHub,
				Purge:      purge,
			}
			if flagOrg != "" { // a single-org purge must never touch shared config/binary
				opts.KeepConfig = true
				opts.KeepBinary = true
			}

			// Always show the plan first - no mutation, no GitHub calls.
			saved := mgr.Config().DryRun
			mgr.Config().DryRun = true
			plan, err := mgr.Uninstall(cmd.Context(), opts)
			mgr.Config().DryRun = saved
			if err != nil {
				return err
			}
			printUninstallPlan(plan)
			if flagDryRun {
				return nil
			}

			if !yes {
				host, _ := os.Hostname()
				fmt.Printf("\nThis is destructive and irreversible. Type the hostname (%s) to proceed: ", host)
				var in string
				_, _ = fmt.Scanln(&in)
				if in != host {
					return fmt.Errorf("aborted (hostname did not match)")
				}
			}

			rep, err := mgr.Uninstall(cmd.Context(), opts)
			if err != nil {
				return err
			}
			printUninstallResult(rep)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the typed-hostname confirmation (for automation)")
	cmd.Flags().BoolVar(&force, "force", false, "proceed even if a runner is busy or a lane is mid-cycle")
	cmd.Flags().BoolVar(&keepConfig, "keep-config", false, "preserve /etc/srm even with --purge")
	cmd.Flags().BoolVar(&keepBinary, "keep-binary", false, "preserve the srm binary")
	cmd.Flags().BoolVar(&keepGitHub, "keep-github", false, "host-side teardown only; make no GitHub calls")
	cmd.Flags().BoolVar(&purge, "purge", false, "also remove /etc/srm (config + App keys) after an automatic backup")
	return cmd
}

func printUninstallPlan(r service.UninstallReport) {
	fmt.Println("srm uninstall - plan:")
	uninstallList("persistent runners", r.Persistent)
	uninstallList("ephemeral lanes", r.Ephemeral)
	uninstallList("service users", r.Users)
	uninstallList("paths", r.Paths)
	if r.ConfigDir != "" {
		fmt.Println("  config dir:", r.ConfigDir, "(backed up first)")
	}
	if r.Binary != "" {
		fmt.Println("  binary:", r.Binary)
	}
	uninstallList("skipped (left untouched)", r.Skipped)
}

func printUninstallResult(r service.UninstallReport) {
	fmt.Printf("\nremoved: %d runner(s), %d lane(s), %d user(s), %d path(s)\n",
		len(r.Persistent), len(r.Ephemeral), len(r.Users), len(r.Paths))
	if r.BackupPath != "" {
		fmt.Println("config backed up to:", r.BackupPath)
	}
	if r.ConfigDir != "" {
		fmt.Println("removed config dir:", r.ConfigDir)
	}
	if r.Binary != "" {
		fmt.Println("removed binary:", r.Binary)
	}
	uninstallList("skipped", r.Skipped)
	uninstallList("errors (non-fatal)", r.Errors)
}

func uninstallList(label string, xs []string) {
	if len(xs) == 0 {
		return
	}
	fmt.Printf("  %s (%d):\n", label, len(xs))
	for _, x := range xs {
		fmt.Println("    -", x)
	}
}
