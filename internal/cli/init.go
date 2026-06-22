package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	huh "charm.land/huh/v2"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/secrets"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Interactively configure an organization (GitHub App auth)",
		Long: "Walks through configuring one organization's GitHub App credentials and " +
			"runner defaults, writes the config file, then verifies the credentials " +
			"against GitHub. Run once per org (also used to re-init or add another org). " +
			"See docs/GITHUB_APP_SETUP.md for how to obtain the App ID, installation ID, and key.",
		RunE: func(*cobra.Command, []string) error { return runInit() },
	}
}

// orgFields holds the raw string inputs the interactive org form binds to. It is
// the single source of truth for the form shared by `srm init` and the first-run
// wizard, so both collect identical fields with identical validation.
type orgFields struct {
	name        string
	appIDStr    string
	instIDStr   string
	keyPath     string
	groupIDStr  string
	labelsStr   string
	installRoot string
}

// defaultOrgFields seeds the form with the conventional defaults.
func defaultOrgFields() orgFields {
	return orgFields{
		groupIDStr: "1",
		// OS/arch defaults track the build (GitHub also auto-adds the read-only
		// self-hosted/<OS>/<arch> labels on top); runtime.GOOS is "linux"/"windows".
		labelsStr:   "self-hosted," + runtime.GOOS + ",x64",
		installRoot: config.DefaultInstallRoot,
	}
}

// orgForm builds the interactive org form bound to f. Defined once so `srm init`
// and the first-run wizard present the exact same prompts and validation.
func orgForm(f *orgFields) *huh.Form {
	required := func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("required")
		}
		return nil
	}
	intRequired := func(s string) error {
		if err := required(s); err != nil {
			return err
		}
		if _, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err != nil {
			return fmt.Errorf("must be a number")
		}
		return nil
	}
	return huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("Organization slug (login)").Placeholder("acme").Value(&f.name).Validate(required),
			huh.NewInput().Title("GitHub App ID").Placeholder("123456").Value(&f.appIDStr).Validate(intRequired),
			huh.NewInput().Title("Installation ID (for this org)").Placeholder("7654321").Value(&f.instIDStr).Validate(intRequired),
			huh.NewInput().Title("Path to the App private key (.pem)").Placeholder(filepath.Join(filepath.Dir(systemConfigPath), "acme.pem")).Value(&f.keyPath).Validate(required),
		),
		huh.NewGroup(
			huh.NewInput().Title("Default runner group ID").Value(&f.groupIDStr).Validate(intRequired),
			huh.NewInput().Title("Default labels (comma-separated)").Value(&f.labelsStr),
			huh.NewInput().Title("Runner install root").Value(&f.installRoot),
		),
	)
}

// toOrgConfig parses collected fields into an OrgConfig (numbers already validated
// by the form).
func (f orgFields) toOrgConfig() config.OrgConfig {
	appID, _ := strconv.ParseInt(strings.TrimSpace(f.appIDStr), 10, 64)
	instID, _ := strconv.ParseInt(strings.TrimSpace(f.instIDStr), 10, 64)
	groupID, _ := strconv.ParseInt(strings.TrimSpace(f.groupIDStr), 10, 64)
	return config.OrgConfig{
		Name:           strings.TrimSpace(f.name),
		AppID:          appID,
		InstallationID: instID,
		PrivateKeyPath: strings.TrimSpace(f.keyPath),
		DefaultGroupID: groupID,
		DefaultLabels:  splitCSV(f.labelsStr),
		InstallRoot:    strings.TrimSpace(f.installRoot),
	}
}

// collectOrg runs the interactive org form (plus the optional key-encryption
// prompt) and returns the resulting OrgConfig. Shared by `srm init` and the
// first-run wizard.
func collectOrg(defaults orgFields) (config.OrgConfig, error) {
	f := defaults
	if err := orgForm(&f).Run(); err != nil {
		return config.OrgConfig{}, err
	}
	oc := f.toOrgConfig()

	// Optionally encrypt the private key into the age secrets store so the .pem
	// no longer needs to live on disk. Only offered when a passphrase is set.
	if os.Getenv("SRM_SECRETS_PASSPHRASE") != "" {
		var importKey bool
		if err := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title("Encrypt the private key into srm's secrets file (removes the .pem dependency)?").
				Value(&importKey),
		)).Run(); err != nil {
			return config.OrgConfig{}, err
		}
		if importKey {
			if err := importPrivateKey(&oc); err != nil {
				return config.OrgConfig{}, err
			}
		}
	}
	return oc, nil
}

func runInit() error {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		return err
	}

	oc, err := collectOrg(defaultOrgFields())
	if err != nil {
		return err
	}

	upsertOrg(cfg, oc)
	if err := writeConfig(flagConfig, cfg); err != nil {
		return err
	}
	fmt.Printf("Wrote %s (org %q).\n", flagConfig, oc.Name)

	// Verify the credentials actually reach GitHub. A write that "succeeded" but
	// can't authenticate is the most common first-run trap (wrong installation id,
	// unreadable key), so surface it now rather than at first TUI load.
	if err := validateOrgAuth(oc.Name); err != nil {
		fmt.Printf("⚠ config written, but GitHub auth check failed: %v\n", err)
		fmt.Println("Fix the App credentials and re-run `srm init`, or `srm doctor` to recheck.")
		return nil
	}
	fmt.Printf("✓ GitHub auth verified for %q.\n", oc.Name)
	fmt.Println("Next: `srm` for the TUI.")
	return nil
}

// firstRunSetup is the guided wizard bare `srm` runs when no orgs are configured
// yet: it collects one or more orgs through the same form as `srm init`, writes
// the config, and verifies each org's GitHub auth - so a brand-new user never has
// to hand-edit config.yaml before reaching a working TUI. It returns nil after a
// successful write (the caller reloads to pick up the orgs) and also when the user
// aborts (a no-op exit), distinguished by whether any org was written.
func firstRunSetup() error {
	fmt.Println("⬢ srm - first-run setup")
	fmt.Printf("No organizations are configured yet. Let's add one.\n"+
		"This writes %s (see docs/GITHUB_APP_SETUP.md for the App ID, installation ID, and key).\n\n", flagConfig)

	cfg, err := config.Load(flagConfig)
	if err != nil {
		return err
	}

	for {
		oc, err := collectOrg(defaultOrgFields())
		if err != nil {
			// huh returns ErrUserAborted on ctrl+c / esc - treat as a clean cancel.
			if errors.Is(err, huh.ErrUserAborted) {
				fmt.Println("Setup cancelled - run `srm init` when you're ready.")
				return nil
			}
			return err
		}
		upsertOrg(cfg, oc)
		if err := writeConfig(flagConfig, cfg); err != nil {
			return err
		}
		fmt.Printf("Wrote %s (org %q).\n", flagConfig, oc.Name)

		if err := validateOrgAuth(oc.Name); err != nil {
			fmt.Printf("\n✗ GitHub auth check failed for %q: %v\n\n", oc.Name, err)
			var retry bool
			if ferr := huh.NewForm(huh.NewGroup(
				huh.NewConfirm().Title("Re-enter this org's settings?").
					Description("No keeps what was written and continues anyway.").
					Value(&retry),
			)).Run(); ferr != nil {
				if errors.Is(ferr, huh.ErrUserAborted) {
					return nil
				}
				return ferr
			}
			if retry {
				continue
			}
		} else {
			fmt.Printf("✓ GitHub auth verified for %q.\n", oc.Name)
		}

		var another bool
		if ferr := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title("Add another organization?").Value(&another),
		)).Run(); ferr != nil {
			if errors.Is(ferr, huh.ErrUserAborted) {
				return nil
			}
			return ferr
		}
		if !another {
			return nil
		}
	}
}

func importPrivateKey(oc *config.OrgConfig) error {
	data, err := os.ReadFile(oc.PrivateKeyPath)
	if err != nil {
		return fmt.Errorf("read key for import: %w", err)
	}
	st, err := secrets.NewAgeFileStore(secretsPath(), os.Getenv("SRM_SECRETS_PASSPHRASE"))
	if err != nil {
		return err
	}
	if err := st.Set("app_key:"+oc.Name, string(data)); err != nil {
		return err
	}
	oc.PrivateKeyPath = "" // now resolved from the secrets store
	fmt.Printf("Stored private key encrypted at %s (key app_key:%s).\n", secretsPath(), oc.Name)
	return nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func upsertOrg(cfg *config.Config, oc config.OrgConfig) {
	for i := range cfg.Orgs {
		if cfg.Orgs[i].Name == oc.Name {
			cfg.Orgs[i] = oc
			return
		}
	}
	cfg.Orgs = append(cfg.Orgs, oc)
}

func writeConfig(path string, cfg *config.Config) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
