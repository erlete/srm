// Package setup holds the shared, UI-agnostic org-onboarding form primitives used by
// both `srm init` (the CLI) and the TUI onboard wizard, so both collect identical
// fields with identical validation and conversion.
package setup

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	huh "charm.land/huh/v2"

	"github.com/erlete/srm/internal/config"
)

// KeyMode selects how the App private key is provided: pasted inline (stored
// encrypted in srm's secrets file, no .pem on disk), referenced by filesystem path,
// or (edit only) left as-is. KeyModeKeep is offered only when editing an existing org.
const (
	KeyModePaste = "paste"
	KeyModePath  = "path"
	KeyModeKeep  = "keep"
)

// OrgFields holds the raw string inputs the interactive org form binds to. It is the
// single source of truth for the form shared by the CLI and the TUI.
type OrgFields struct {
	Name       string
	AppIDStr   string
	InstIDStr  string
	KeyMode    string // KeyModePaste (default), KeyModePath, or KeyModeKeep (edit only)
	KeyPaste   string // pasted PEM contents (paste mode); stored encrypted, never in config
	KeyPath    string // path to a .pem on disk (path mode)
	// CurrentKeyPath is the org's existing PrivateKeyPath, carried through KeyModeKeep
	// so an edit that keeps the current key preserves it (empty = key is in the store).
	CurrentKeyPath string
	GroupIDStr     string
	LabelsStr  string
	// Passphrase / PassphraseConfirm are collected ONLY when paste mode needs to
	// encrypt but no secrets passphrase exists yet. They are consumed to seed the age
	// store and then discarded - never persisted to config.
	Passphrase        string
	PassphraseConfirm string
	InstallRoot       string
}

// DefaultOrgFields seeds the form with the conventional defaults. Paste is the
// recommended key mode: it drops the .pem-on-disk dependency entirely.
func DefaultOrgFields() OrgFields {
	return OrgFields{
		KeyMode:    KeyModePaste,
		GroupIDStr: "1",
		// OS/arch defaults track the build (GitHub also auto-adds the read-only
		// self-hosted/<OS>/<arch> labels on top); runtime.GOOS is "linux"/"windows".
		LabelsStr:   "self-hosted," + runtime.GOOS + ",x64",
		InstallRoot: config.DefaultInstallRoot,
	}
}

// ValidatePEM reports whether s parses as a private key in PEM form (PKCS#1 or
// PKCS#8) - the shape of a GitHub App private key. Its errors are deliberately
// generic so the key material never reaches an error string or a log.
func ValidatePEM(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("required")
	}
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return fmt.Errorf("not a PEM block (expected -----BEGIN ... PRIVATE KEY-----)")
	}
	if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return nil
	}
	if _, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return nil
	}
	return fmt.Errorf("not a valid private key (expected an RSA App key in PKCS#1 or PKCS#8 PEM)")
}

// OrgForm builds the interactive org form bound to f. keyDir seeds the private-key
// path placeholder (the config dir); pass "" for a bare placeholder. passphraseKnown
// tells the form whether a secrets passphrase already exists (env var or persisted
// file): when it does not, paste mode collects one so the key can be encrypted.
// Defined once so the CLI and TUI present the exact same prompts and validation.
func OrgForm(f *OrgFields, keyDir string, passphraseKnown, edit bool) *huh.Form {
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
	keyPlaceholder := "acme.pem"
	if keyDir != "" {
		keyPlaceholder = filepath.Join(keyDir, "acme.pem")
	}
	usePaste := func() bool { return f.KeyMode == KeyModePaste }
	usePath := func() bool { return f.KeyMode == KeyModePath }
	needPassphrase := func() bool { return usePaste() && !passphraseKnown }

	// The org slug is the identity key: editable when onboarding, locked when editing
	// (changing it would upsert a different org and orphan this one).
	var nameField huh.Field
	if edit {
		nameField = huh.NewNote().Title("Organization").Description(f.Name)
	} else {
		nameField = huh.NewInput().Title("Organization slug (login)").Placeholder("acme").Value(&f.Name).Validate(required)
	}
	// Key options: onboarding offers paste/path; editing also offers keep-current (the
	// default), since a store-encrypted key can't be pre-filled and shouldn't be re-typed.
	keyOpts := []huh.Option[string]{
		huh.NewOption("Paste the key contents (stored encrypted, no .pem on disk)", KeyModePaste),
		huh.NewOption("Reference a .pem file by path", KeyModePath),
	}
	if edit {
		keyOpts = append([]huh.Option[string]{huh.NewOption("Keep the current key", KeyModeKeep)}, keyOpts...)
	}
	return huh.NewForm(
		huh.NewGroup(
			nameField,
			huh.NewInput().Title("GitHub App ID").Placeholder("123456").Value(&f.AppIDStr).Validate(intRequired),
			huh.NewInput().Title("Installation ID (for this org)").Placeholder("7654321").Value(&f.InstIDStr).Validate(intRequired),
			huh.NewSelect[string]().Title("App private key").Options(keyOpts...).Value(&f.KeyMode),
		),
		huh.NewGroup(
			huh.NewNote().Description("Paste the App private key. It is stored encrypted in srm's secrets file and never written to config.yaml or any log."),
			huh.NewText().Title("App private key (PEM)").
				Placeholder("-----BEGIN RSA PRIVATE KEY-----\n...").
				Lines(6).CharLimit(1<<16).
				Value(&f.KeyPaste).Validate(ValidatePEM),
		).WithHideFunc(func() bool { return !usePaste() }),
		huh.NewGroup(
			huh.NewInput().Title("Path to the App private key (.pem)").Placeholder(keyPlaceholder).Value(&f.KeyPath).Validate(required),
		).WithHideFunc(func() bool { return !usePath() }),
		huh.NewGroup(
			huh.NewNote().Description("No secrets passphrase is set yet. Choose one to encrypt the key. It is saved root-only so srm's background runner units can decrypt without prompting."),
			huh.NewInput().Title("Secrets passphrase").EchoMode(huh.EchoModePassword).Value(&f.Passphrase).Validate(func(s string) error {
				if len(s) < 8 {
					return fmt.Errorf("use at least 8 characters")
				}
				return nil
			}),
			huh.NewInput().Title("Confirm passphrase").EchoMode(huh.EchoModePassword).Value(&f.PassphraseConfirm).Validate(func(s string) error {
				if s != f.Passphrase {
					return fmt.Errorf("passphrases do not match")
				}
				return nil
			}),
		).WithHideFunc(func() bool { return !needPassphrase() }),
		huh.NewGroup(
			huh.NewInput().Title("Default runner group ID").Value(&f.GroupIDStr).Validate(intRequired),
			huh.NewInput().Title("Default labels (comma-separated)").Value(&f.LabelsStr),
			huh.NewInput().Title("Runner install root").Value(&f.InstallRoot),
		),
		// A bounded width keeps the modal compact: without it huh expands to the full
		// terminal and vertically justifies the group's fields (big gaps). Matches the
		// settings/uninstall forms.
	).WithWidth(66)
}

// ToOrgConfig parses collected fields into an OrgConfig (numbers already validated by
// the form). Paste mode leaves PrivateKeyPath empty (the key resolves from the secrets
// store under "app_key:<org>"; the caller stores it via StoreOrgKey); keep mode carries
// the org's current PrivateKeyPath through unchanged.
func (f OrgFields) ToOrgConfig() config.OrgConfig {
	appID, _ := strconv.ParseInt(strings.TrimSpace(f.AppIDStr), 10, 64)
	instID, _ := strconv.ParseInt(strings.TrimSpace(f.InstIDStr), 10, 64)
	groupID, _ := strconv.ParseInt(strings.TrimSpace(f.GroupIDStr), 10, 64)
	keyPath := strings.TrimSpace(f.KeyPath)
	switch f.KeyMode {
	case KeyModePaste:
		keyPath = ""
	case KeyModeKeep:
		keyPath = f.CurrentKeyPath
	}
	return config.OrgConfig{
		Name:           strings.TrimSpace(f.Name),
		AppID:          appID,
		InstallationID: instID,
		PrivateKeyPath: keyPath,
		DefaultGroupID: groupID,
		DefaultLabels:  SplitCSV(f.LabelsStr),
		InstallRoot:    strings.TrimSpace(f.InstallRoot),
	}
}

// FieldsFromOrgConfig seeds the form from an existing org for the edit flow: numbers
// stringified, labels re-joined, and the key defaulted to "keep the current key"
// (CurrentKeyPath preserves the existing .pem path, if any, so ToOrgConfig round-trips
// an untouched key). InstallRoot falls back to the default when the org left it unset.
func FieldsFromOrgConfig(oc config.OrgConfig) OrgFields {
	installRoot := oc.InstallRoot
	if strings.TrimSpace(installRoot) == "" {
		installRoot = config.DefaultInstallRoot
	}
	return OrgFields{
		Name:           oc.Name,
		AppIDStr:       strconv.FormatInt(oc.AppID, 10),
		InstIDStr:      strconv.FormatInt(oc.InstallationID, 10),
		KeyMode:        KeyModeKeep,
		KeyPath:        oc.PrivateKeyPath,
		CurrentKeyPath: oc.PrivateKeyPath,
		GroupIDStr:     strconv.FormatInt(oc.DefaultGroupID, 10),
		LabelsStr:      strings.Join(oc.DefaultLabels, ","),
		InstallRoot:    installRoot,
	}
}

// SplitCSV splits and trims a comma-separated list, dropping empty entries.
func SplitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
