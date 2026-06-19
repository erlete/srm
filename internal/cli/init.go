package cli

import (
	"fmt"
	"os"
	"path/filepath"
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
			"runner defaults, then writes the config file. Run once per org. " +
			"See docs/GITHUB_APP_SETUP.md for how to obtain the App ID, installation ID, and key.",
		RunE: func(*cobra.Command, []string) error { return runInit() },
	}
}

func runInit() error {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		return err
	}

	var (
		name        string
		appIDStr    string
		instIDStr   string
		keyPath     string
		groupIDStr  = "1"
		labelsStr   = "self-hosted,linux,x64"
		installRoot = "/opt/actions-runners"
	)

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

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("Organization slug (login)").Placeholder("acme").Value(&name).Validate(required),
			huh.NewInput().Title("GitHub App ID").Placeholder("123456").Value(&appIDStr).Validate(intRequired),
			huh.NewInput().Title("Installation ID (for this org)").Placeholder("7654321").Value(&instIDStr).Validate(intRequired),
			huh.NewInput().Title("Path to the App private key (.pem)").Placeholder("/etc/srm/acme.pem").Value(&keyPath).Validate(required),
		),
		huh.NewGroup(
			huh.NewInput().Title("Default runner group ID").Value(&groupIDStr).Validate(intRequired),
			huh.NewInput().Title("Default labels (comma-separated)").Value(&labelsStr),
			huh.NewInput().Title("Runner install root").Value(&installRoot),
		),
	)
	if err := form.Run(); err != nil {
		return err
	}

	appID, _ := strconv.ParseInt(strings.TrimSpace(appIDStr), 10, 64)
	instID, _ := strconv.ParseInt(strings.TrimSpace(instIDStr), 10, 64)
	groupID, _ := strconv.ParseInt(strings.TrimSpace(groupIDStr), 10, 64)

	oc := config.OrgConfig{
		Name:           strings.TrimSpace(name),
		AppID:          appID,
		InstallationID: instID,
		PrivateKeyPath: strings.TrimSpace(keyPath),
		DefaultGroupID: groupID,
		DefaultLabels:  splitCSV(labelsStr),
		InstallRoot:    strings.TrimSpace(installRoot),
	}

	// Optionally encrypt the private key into the age secrets store so the .pem
	// no longer needs to live on disk. Only offered when a passphrase is set.
	if os.Getenv("SRM_SECRETS_PASSPHRASE") != "" {
		var importKey bool
		if err := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title("Encrypt the private key into srm's secrets file (removes the .pem dependency)?").
				Value(&importKey),
		)).Run(); err != nil {
			return err
		}
		if importKey {
			if err := importPrivateKey(&oc); err != nil {
				return err
			}
		}
	}

	upsertOrg(cfg, oc)

	if err := writeConfig(flagConfig, cfg); err != nil {
		return err
	}
	fmt.Printf("Wrote %s (org %q).\n", flagConfig, oc.Name)
	fmt.Println("Next: `srm doctor` to verify auth, then `srm` for the TUI.")
	return nil
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
