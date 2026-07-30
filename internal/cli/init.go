package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	huh "charm.land/huh/v2"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/secrets"
	"github.com/erlete/srm/internal/setup"
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

// collectOrg runs the interactive org form (plus the optional key-encryption
// prompt) and returns the resulting OrgConfig. Shared by `srm init` and the
// first-run wizard. The form itself lives in internal/setup so the TUI onboard
// wizard collects identical fields.
func collectOrg(defaults setup.OrgFields, edit bool) (config.OrgConfig, error) {
	f := defaults
	_, passKnown := secrets.ResolvePassphrase(passFilePath())
	if err := setup.OrgForm(&f, filepath.Dir(systemConfigPath), passKnown, edit).Run(); err != nil {
		return config.OrgConfig{}, err
	}
	oc := f.ToOrgConfig()

	switch f.KeyMode {
	case setup.KeyModeKeep:
		// Edit that keeps the current key: nothing to store (ToOrgConfig preserved the
		// existing PrivateKeyPath / store reference).
	case setup.KeyModePaste:
		// Paste mode always stores encrypted - there is no .pem to reference. The
		// passphrase is the known one, or the one just created in the form (f.Passphrase),
		// which EncryptAppKey persists root-only so the background units can decrypt.
		if _, err := secrets.EncryptAppKey(secretsPath(), passFilePath(), oc.Name, f.KeyPaste, f.Passphrase); err != nil {
			return config.OrgConfig{}, err
		}
		fmt.Printf("Stored the App private key encrypted at %s (key app_key:%s).\n", secretsPath(), oc.Name)
	default:
		// Path mode: optionally encrypt the referenced .pem into the secrets store so
		// the file no longer needs to live on disk. Only offered when a passphrase exists.
		if passKnown {
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
	}
	return oc, nil
}

func runInit() error {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		return err
	}

	oc, err := collectOrg(setup.DefaultOrgFields(), false)
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
		oc, err := collectOrg(setup.DefaultOrgFields(), false)
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
	if _, err := secrets.EncryptAppKey(secretsPath(), passFilePath(), oc.Name, string(data), ""); err != nil {
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
