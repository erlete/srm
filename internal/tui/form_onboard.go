package tui

import (
	huh "charm.land/huh/v2"

	"github.com/erlete/srm/internal/setup"
)

// onboardForm is the TUI onboarding wizard: the same org-credential form `srm init`
// uses (shared via internal/setup), but driven through the Model's event loop. On
// completion the Model upserts the org, persists + reloads the config, and validates
// the new org's GitHub auth.
type onboardForm struct {
	form   *huh.Form
	fields setup.OrgFields
}

func newOnboardForm(keyDir string) *onboardForm {
	of := &onboardForm{fields: setup.DefaultOrgFields()}
	of.form = setup.OrgForm(&of.fields, keyDir)
	return of
}
