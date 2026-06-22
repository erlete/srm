package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newRunnerSupervisorCmd is the hidden worker a Windows ephemeral slot's service
// runs (binPath = `srm-win _runner-supervisor --org X --slot N`). It loops one job
// cycle at a time IN PROCESS until the Service Control Manager stops it - the Windows
// stand-in for systemd Restart=always, which SCM has no analog for (a clean process
// exit is "success", not a relaunch trigger). Not for interactive use; off Windows it
// is unsupported (the command exists in every build only so the mirror cross-compiles).
func newRunnerSupervisorCmd() *cobra.Command {
	var slot string
	c := &cobra.Command{
		Use:    "_runner-supervisor",
		Hidden: true,
		Short:  "Internal: supervise an ephemeral slot's job cycles (Windows service entry point)",
		RunE: func(*cobra.Command, []string) error {
			if flagOrg == "" || slot == "" {
				return fmt.Errorf("--org and --slot are required")
			}
			if flagDryRun {
				return fmt.Errorf("_runner-supervisor runs real jobs and does not support --dry-run")
			}
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			return runSupervisor(mgr, flagOrg, slot)
		},
	}
	c.Flags().StringVar(&slot, "slot", "", "slot id")
	return c
}
