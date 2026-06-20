package cli

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/erlete/srm/internal/service"
)

// newBackupCmd archives the srm config directory (config.yaml + secrets.age +
// per-org App keys) to a tarball. The App private key is shown once by GitHub, so
// this is the recommended snapshot before any destructive lifecycle operation.
func newBackupCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Back up the srm config dir (config.yaml, secrets.age, App keys) to a tarball",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir := filepath.Dir(flagConfig)
			if out == "" {
				out = fmt.Sprintf("srm-backup-%d.tar.gz", time.Now().Unix())
			}
			path, err := service.BackupConfigDir(dir, out)
			if err != nil {
				return err
			}
			fmt.Printf("backed up %s -> %s\n", dir, path)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "output tarball path (default: srm-backup-<unix-ts>.tar.gz)")
	return cmd
}

// newRestoreCmd extracts a backup tarball back into the config directory.
func newRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <archive>",
		Short: "Restore an srm config-dir backup into the config directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir := filepath.Dir(flagConfig)
			if err := service.RestoreConfigDir(args[0], dir); err != nil {
				return err
			}
			fmt.Printf("restored %s -> %s\n", args[0], dir)
			return nil
		},
	}
}
