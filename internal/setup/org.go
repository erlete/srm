// Package setup holds the shared, UI-agnostic org-onboarding form primitives used by
// both `srm init` (the CLI) and the TUI onboard wizard, so both collect identical
// fields with identical validation and conversion.
package setup

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	huh "charm.land/huh/v2"

	"github.com/erlete/srm/internal/config"
)

// OrgFields holds the raw string inputs the interactive org form binds to. It is the
// single source of truth for the form shared by the CLI and the TUI.
type OrgFields struct {
	Name        string
	AppIDStr    string
	InstIDStr   string
	KeyPath     string
	GroupIDStr  string
	LabelsStr   string
	InstallRoot string
}

// DefaultOrgFields seeds the form with the conventional defaults.
func DefaultOrgFields() OrgFields {
	return OrgFields{
		GroupIDStr: "1",
		// OS/arch defaults track the build (GitHub also auto-adds the read-only
		// self-hosted/<OS>/<arch> labels on top); runtime.GOOS is "linux"/"windows".
		LabelsStr:   "self-hosted," + runtime.GOOS + ",x64",
		InstallRoot: config.DefaultInstallRoot,
	}
}

// OrgForm builds the interactive org form bound to f. keyDir seeds the private-key
// placeholder (the config dir); pass "" for a bare placeholder. Defined once so the
// CLI and TUI present the exact same prompts and validation.
func OrgForm(f *OrgFields, keyDir string) *huh.Form {
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
	return huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("Organization slug (login)").Placeholder("acme").Value(&f.Name).Validate(required),
			huh.NewInput().Title("GitHub App ID").Placeholder("123456").Value(&f.AppIDStr).Validate(intRequired),
			huh.NewInput().Title("Installation ID (for this org)").Placeholder("7654321").Value(&f.InstIDStr).Validate(intRequired),
			huh.NewInput().Title("Path to the App private key (.pem)").Placeholder(keyPlaceholder).Value(&f.KeyPath).Validate(required),
		),
		huh.NewGroup(
			huh.NewInput().Title("Default runner group ID").Value(&f.GroupIDStr).Validate(intRequired),
			huh.NewInput().Title("Default labels (comma-separated)").Value(&f.LabelsStr),
			huh.NewInput().Title("Runner install root").Value(&f.InstallRoot),
		),
	)
}

// ToOrgConfig parses collected fields into an OrgConfig (numbers already validated by
// the form).
func (f OrgFields) ToOrgConfig() config.OrgConfig {
	appID, _ := strconv.ParseInt(strings.TrimSpace(f.AppIDStr), 10, 64)
	instID, _ := strconv.ParseInt(strings.TrimSpace(f.InstIDStr), 10, 64)
	groupID, _ := strconv.ParseInt(strings.TrimSpace(f.GroupIDStr), 10, 64)
	return config.OrgConfig{
		Name:           strings.TrimSpace(f.Name),
		AppID:          appID,
		InstallationID: instID,
		PrivateKeyPath: strings.TrimSpace(f.KeyPath),
		DefaultGroupID: groupID,
		DefaultLabels:  SplitCSV(f.LabelsStr),
		InstallRoot:    strings.TrimSpace(f.InstallRoot),
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
