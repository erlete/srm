// Package cli wires the cobra command tree. A bare invocation launches the TUI;
// subcommands run headless and share the same service layer.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/erlete/srm/internal/config"
	applog "github.com/erlete/srm/internal/log"
	"github.com/erlete/srm/internal/secrets"
	"github.com/erlete/srm/internal/service"
	"github.com/erlete/srm/internal/tui"
)

var (
	flagConfig string
	flagOrg    string
	flagDryRun bool
)

// Execute runs the root command. version is the build-stamped release string,
// surfaced via `srm --version` and `srm version`.
func Execute(version string) error {
	root := &cobra.Command{
		Use:           "srm",
		Short:         "Manage GitHub Actions self-hosted runners (TUI + CLI)",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(*cobra.Command, []string) error {
			return runTUI()
		},
	}
	root.PersistentFlags().StringVar(&flagConfig, "config", defaultConfigPath(), "config file path")
	root.PersistentFlags().StringVar(&flagOrg, "org", "", "operate on a single org (default: all configured orgs where applicable)")
	root.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "preview mutations without calling GitHub")

	root.AddCommand(newInitCmd(), newRunnersCmd(), newGroupsCmd(), newProvisionCmd(), newDoctorCmd(), newCacheCmd(), newReconcileCmd(), newRunnerCycleCmd(), newBackupCmd(), newRestoreCmd(), newVersionCmd(version))
	return root.Execute()
}

// newVersionCmd prints the build-stamped version (complements cobra's --version).
func newVersionCmd(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the srm version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			fmt.Println("srm", version)
			return nil
		},
	}
}

// systemConfigPath is the canonical location on a managed host. srm is a
// root/sudo-operated tool, so the system path is preferred over the per-user
// XDG path. Resolution precedence is: --config flag > /etc/srm/config.yaml >
// $HOME/.config/srm/config.yaml.
const systemConfigPath = "/etc/srm/config.yaml"

func defaultConfigPath() string {
	// The system path wins whenever it already exists — that's the standard
	// location srm deploys to, so bare `srm` on a managed host finds it.
	if _, err := os.Stat(systemConfigPath); err == nil {
		return systemConfigPath
	}
	// On a fresh host, root (sudo) defaults to the system path so `sudo srm init`
	// seeds /etc/srm rather than root's home. (Geteuid is -1 on Windows, so the
	// dev box falls through to the XDG path below.)
	if os.Geteuid() == 0 {
		return systemConfigPath
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return "srm.yaml"
	}
	return filepath.Join(dir, "srm", "config.yaml")
}

// secretsPath co-locates the age secrets file with the active config file, so
// /etc/srm holds both config.yaml and secrets.age on a managed host.
func secretsPath() string {
	if flagConfig != "" {
		return filepath.Join(filepath.Dir(flagConfig), "secrets.age")
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return "srm-secrets.age"
	}
	return filepath.Join(dir, "srm", "secrets.age")
}

// buildManager loads config + secrets + logging and returns a service.Manager
// plus a log closer.
func buildManager() (*service.Manager, func() error, error) {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		return nil, nil, err
	}
	if flagDryRun {
		cfg.DryRun = true
	}

	_, closeLog, err := applog.New(cfg.LogFile)
	if err != nil {
		return nil, nil, err
	}

	mgr := service.New(cfg, resolveSecrets(), flagConfig)
	return mgr, closeLog, nil
}

// requireOrgs returns a friendly error when no orgs are configured.
func requireOrgs(mgr *service.Manager) error {
	if len(mgr.OrgNames()) == 0 {
		return noOrgsError()
	}
	return nil
}

// targetOrg resolves the single org a write applies to. There is no hidden
// "active org": it is --org if given, else the sole configured org, else an
// error demanding --org. This stops a mutation from silently hitting the wrong
// org when several are configured.
func targetOrg(mgr *service.Manager) (string, error) {
	if flagOrg != "" {
		if _, ok := mgr.Config().Org(flagOrg); !ok {
			return "", fmt.Errorf("org %q not configured", flagOrg)
		}
		return flagOrg, nil
	}
	names := mgr.OrgNames()
	if len(names) == 1 {
		return names[0], nil
	}
	return "", fmt.Errorf("multiple orgs configured (%s) — specify --org", strings.Join(names, ", "))
}

// noOrgsError crafts the right guidance when no orgs resolved. On a managed host
// the config + App key live root-only under /etc/srm, so a non-root invocation
// can't read them — point the user at sudo rather than the irrelevant per-user
// path. (Geteuid is -1 on Windows, so this only triggers on the real hosts.)
func noOrgsError() error {
	if os.Geteuid() != 0 {
		if _, err := os.Stat(systemConfigPath); err == nil || errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("the system config %s is root-only (srm is sudo-operated) — re-run with sudo, e.g. `sudo srm runners list`", systemConfigPath)
		}
	}
	return fmt.Errorf("no orgs configured — create %s (see config.example.yaml)", flagConfig)
}

// resolveSecrets prefers an age-encrypted file when SRM_SECRETS_PASSPHRASE is
// set; otherwise it falls back to environment variables.
func resolveSecrets() secrets.Store {
	if pass := os.Getenv("SRM_SECRETS_PASSPHRASE"); pass != "" {
		if st, err := secrets.NewAgeFileStore(secretsPath(), pass); err == nil {
			return st
		}
	}
	return secrets.EnvStore{}
}

func runTUI() error {
	mgr, closeLog, err := buildManager()
	if err != nil {
		return err
	}
	defer closeLog()

	if len(mgr.OrgNames()) == 0 {
		return noOrgsError()
	}

	p := tea.NewProgram(tui.New(context.Background(), mgr))
	_, err = p.Run()
	return err
}
