package tui

import (
	huh "charm.land/huh/v2"

	"github.com/erlete/srm/internal/service"
)

// Uninstall option keys for the MultiSelect (value = key, label = human text). The
// default (nothing selected) is the fail-safe full-host teardown that LEAVES /etc/srm
// in place; opts() maps the selected keys onto UninstallOpts.
const (
	uoPurge      = "purge"
	uoKeepConfig = "keepconfig"
	uoKeepBinary = "keepbinary"
	uoKeepGitHub = "keepgithub"
	uoForce      = "force"
)

// fullHostScope is the scope-select value standing for "" (every configured org / the
// whole host); "" is not a distinguishable option value, so a sentinel is used and
// mapped back in opts().
const fullHostScope = "\x00full-host"

// uninstallForm is Step 1 of a guarded uninstall: it collects the scope and the
// KeepConfig/KeepBinary/KeepGitHub/Force/Purge toggles the CLI exposes, which the TUI
// previously never surfaced. Its completion runs the dry-run blast-radius preview;
// the destructive gates (typed hostname, a second PURGE token, and the writable-backup
// pre-check) live downstream and only escalate when purge would remove /etc/srm.
type uninstallForm struct {
	form    *huh.Form
	org     string
	options []string
}

func newUninstallForm(orgs []string, currentOrg string) *uninstallForm {
	uf := &uninstallForm{org: currentOrg}
	if uf.org == "" {
		uf.org = fullHostScope
	}
	uf.form = huh.NewForm(
		huh.NewGroup(
			huh.NewNote().Title("Uninstall").Description("Removes only srm-attributable artifacts on THIS host. Defaults are fail-safe: /etc/srm and its App keys stay unless you select purge (which then requires extra confirmation)."),
			huh.NewSelect[string]().Title("Scope").Options(uninstallScopeOptions(orgs)...).Value(&uf.org),
			huh.NewMultiSelect[string]().Title("Options").Options(uninstallOptionChoices()...).Value(&uf.options),
		),
	).WithWidth(66)
	return uf
}

func uninstallScopeOptions(orgs []string) []huh.Option[string] {
	opts := []huh.Option[string]{huh.NewOption("full host (every configured org)", fullHostScope)}
	for _, o := range orgs {
		opts = append(opts, huh.NewOption("org "+o+" only", o))
	}
	return opts
}

func uninstallOptionChoices() []huh.Option[string] {
	return []huh.Option[string]{
		huh.NewOption("purge - also delete /etc/srm (config + App keys) after a backup", uoPurge),
		huh.NewOption("keep /etc/srm even under purge", uoKeepConfig),
		huh.NewOption("keep the srm binary", uoKeepBinary),
		huh.NewOption("keep GitHub registrations (host-side teardown only)", uoKeepGitHub),
		huh.NewOption("force - proceed despite a busy runner / in-flight lane", uoForce),
	}
}

// opts maps the form selections onto a service.UninstallOpts.
func (uf *uninstallForm) opts() service.UninstallOpts {
	has := map[string]bool{}
	for _, o := range uf.options {
		has[o] = true
	}
	org := uf.org
	if org == fullHostScope {
		org = ""
	}
	return service.UninstallOpts{
		Org:        org,
		Purge:      has[uoPurge],
		KeepConfig: has[uoKeepConfig],
		KeepBinary: has[uoKeepBinary],
		KeepGitHub: has[uoKeepGitHub],
		Force:      has[uoForce],
	}
}
