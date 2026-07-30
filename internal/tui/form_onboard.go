package tui

import (
	huh "charm.land/huh/v2"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/setup"
)

// onboardForm is the TUI org-credential wizard: the same form `srm init` uses (shared
// via internal/setup), driven through the Model's event loop. It backs both onboarding
// a NEW org and editing an EXISTING one (prefilled, key defaults to keep) - on
// completion the Model upserts the org, stores a pasted key, persists + reloads the
// config, and validates GitHub auth. The two flows differ only in how it is seeded.
type onboardForm struct {
	form   *huh.Form
	fields setup.OrgFields
	edit   bool // true = editing an existing org (drives the modal title + status wording)
}

func newOnboardForm(keyDir string, passphraseKnown bool) *onboardForm {
	of := &onboardForm{fields: setup.DefaultOrgFields()}
	of.form = setup.OrgForm(&of.fields, keyDir, passphraseKnown, false)
	return of
}

// newEditOrgForm seeds the shared form from an existing org (edit mode: slug locked,
// key defaults to keep-current). Reuses the onboard save path, so a re-key by paste is
// stored encrypted and keep/path leave the key untouched.
func newEditOrgForm(oc config.OrgConfig, keyDir string, passphraseKnown bool) *onboardForm {
	of := &onboardForm{fields: setup.FieldsFromOrgConfig(oc), edit: true}
	of.form = setup.OrgForm(&of.fields, keyDir, passphraseKnown, true)
	return of
}
