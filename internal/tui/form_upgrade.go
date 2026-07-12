package tui

import (
	"errors"
	"strings"

	huh "charm.land/huh/v2"
)

// upgradeForm collects the two knobs the CLI exposes as `--to-version` and
// `--force`, shown before the dry-run preview: a target agent version (blank = the
// configured pin, else the version GitHub publishes) and a force toggle (reinstall
// at the same version / allow a downgrade). Scope (org or a runner selection) is
// decided by the caller, not here.
type upgradeForm struct {
	form    *huh.Form
	version string
	force   bool
}

func newUpgradeForm() *upgradeForm {
	uf := &upgradeForm{}
	uf.form = huh.NewForm(
		huh.NewGroup(
			huh.NewNote().Title("Upgrade options").Description("Blank version = the configured pin, else the version GitHub publishes."),
			huh.NewInput().Title("Target version").Placeholder("(default)").Value(&uf.version).Validate(validateOptionalAgentVersion),
			huh.NewConfirm().Title("Force").Description("Reinstall at the same version, and allow a downgrade.").Value(&uf.force),
		),
	).WithWidth(62)
	return uf
}

// toVersion is the normalized UpgradeOpts.ToVersion: trimmed, with a tolerated
// leading "v" stripped (DownloadURL adds its own "v"); "" means "use the default".
func (uf *upgradeForm) toVersion() string { return normalizeAgentVersion(uf.version) }

// normalizeAgentVersion strips surrounding space and a single tolerated leading "v"
// so both "2.335.1" and "v2.335.1" resolve to the same agent version.
func normalizeAgentVersion(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "v")
}

// validateOptionalAgentVersion accepts an empty string (use the default) or a
// dotted non-negative-integer agent version like "2.335.1" (a leading "v" is
// tolerated). It is a shape check only: a well-formed but nonexistent version is
// refused later, when the download's checksum can't be resolved. A bare "v" and a
// signed or non-numeric field are both rejected.
func validateOptionalAgentVersion(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil // blank = use the default
	}
	v := normalizeAgentVersion(s)
	if v == "" {
		return errBadAgentVersion // input was just "v"
	}
	for field := range strings.SplitSeq(v, ".") {
		if !allDigits(field) {
			return errBadAgentVersion
		}
	}
	return nil
}

// allDigits reports whether s is a non-empty run of ASCII digits (no sign, no
// separators) - so "-1", "+2", "1a" and "" all fail.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

var errBadAgentVersion = errors.New("version must be numeric dotted, e.g. 2.335.1 (or blank for the default)")
