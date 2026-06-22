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
	"time"

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

	root.AddCommand(newInitCmd(), newRunnersCmd(), newGroupsCmd(), newProvisionCmd(), newDoctorCmd(), newCacheCmd(), newReconcileCmd(), newRunnerCycleCmd(), newRunnerSupervisorCmd(), newBackupCmd(), newRestoreCmd(), newUninstallCmd(), newVersionCmd(version))
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

// systemConfigPath is the canonical machine-wide config location on a managed host.
// srm is an elevated-operated tool, so the system path is preferred over the per-user
// path. Its OS-specific value (Linux /etc/srm/config.yaml; Windows
// %ProgramData%\srm\config.yaml) lives in root_linux.go / root_windows.go. Resolution
// precedence is: --config flag > system path (present, or running elevated) > per-user
// config dir.

func defaultConfigPath() string {
	// The system path wins whenever it already exists - that's the standard
	// location srm deploys to, so bare `srm` on a managed host finds it.
	if _, err := os.Stat(systemConfigPath); err == nil {
		return systemConfigPath
	}
	// On a fresh host, an elevated invocation (sudo / Administrator) defaults to the
	// system path so `srm init` seeds the machine-wide dir rather than a user profile.
	if isElevated() {
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
	return "", fmt.Errorf("multiple orgs configured (%s) - specify --org", strings.Join(names, ", "))
}

// noOrgsError crafts the right guidance when no orgs resolved. On a managed host the
// config + App key live in the restricted machine-wide dir, so an unprivileged
// invocation can't read them - point the user at re-running elevated rather than the
// irrelevant per-user path.
func noOrgsError() error {
	if !isElevated() {
		if _, err := os.Stat(systemConfigPath); err == nil || errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("the system config %s is restricted (srm is elevated-operated) - re-run elevated (sudo on Linux, Administrator on Windows), e.g. `sudo srm runners list`", systemConfigPath)
		}
	}
	return fmt.Errorf("no orgs configured - create %s (see config.example.yaml)", flagConfig)
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
	// First run: guide the user through configuring an org so they never have to
	// hand-edit config.yaml. ensureConfigured is a no-op once any org exists.
	if err := ensureConfigured(); err != nil {
		return err
	}

	mgr, closeLog, err := buildManager()
	if err != nil {
		return err
	}
	defer closeLog()

	if len(mgr.OrgNames()) == 0 {
		// Setup was cancelled (or wrote nothing); ensureConfigured already printed
		// the guidance, so just exit quietly rather than launching an empty TUI.
		return nil
	}

	p := tea.NewProgram(tui.New(context.Background(), mgr))
	_, err = p.Run()
	return err
}

// ensureConfigured runs the guided first-run wizard when no orgs are configured
// and a fresh setup is possible here; it is a no-op once orgs exist. An unreadable
// config (e.g. the root-only system file accessed as a non-root user) steers to
// the sudo hint instead of a wizard that couldn't persist anyway.
func ensureConfigured() error {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return noOrgsError()
		}
		return err
	}
	if len(cfg.OrgNames()) > 0 {
		return nil
	}
	if !firstRunEligible() {
		return noOrgsError()
	}
	return firstRunSetup()
}

// firstRunEligible reports whether a no-orgs situation is a genuine fresh install we
// can guide and write - as opposed to a restricted system config we merely can't read
// unprivileged, where the right answer is "re-run elevated". It is the inverse of
// noOrgsError's elevated-hint condition.
func firstRunEligible() bool {
	if !isElevated() {
		if _, err := os.Stat(systemConfigPath); err == nil || errors.Is(err, fs.ErrPermission) {
			return false
		}
	}
	return true
}

// validateOrgAuth confirms an org's GitHub App credentials actually authenticate
// by listing its runners through a freshly built manager - so it reads the config
// just written to disk. A nil error means auth is good (0 runners still counts).
func validateOrgAuth(org string) error {
	mgr, closeLog, err := buildManager()
	if err != nil {
		return err
	}
	defer closeLog()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = mgr.ListRunners(ctx, org)
	return err
}
