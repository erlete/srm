package tui

import (
	"fmt"
	"strconv"
	"strings"

	huh "charm.land/huh/v2"

	"github.com/erlete/srm/internal/core"
)

// profileForm is the Lifecycle "Manage profiles" editor: create-or-replace a per-org
// create-preset by name, or delete it. It defines a profile's PUBLIC fields
// (labels/group/nature/container); the per-profile dependency manifest and other
// metadata are preserved verbatim on save (saveProfileCmd overlays onto the existing
// profile) - so editing here never silently drops a manifest. Precise single-field
// edits live in `srm config profiles set` (Changed-overlay); this is the full
// definition.
type profileForm struct {
	form      *huh.Form
	org       string
	name      string
	ephemeral bool
	labels    string
	groupID   string
	container string
	del       bool
}

func newProfileForm(orgs []string) *profileForm {
	pf := &profileForm{}
	if len(orgs) > 0 {
		pf.org = orgs[0]
	}
	pf.form = huh.NewForm(
		huh.NewGroup(
			huh.NewNote().Title("Manage create-presets").Description("Create or replace a profile by name (or delete it). Used by the create wizard's Profile select and `srm runners create --profile`. A per-profile dependency manifest, if any, is preserved."),
			huh.NewSelect[string]().Title("Org").Options(orgOptions(orgs)...).Value(&pf.org),
			huh.NewInput().Title("Profile name").Placeholder("gpu").Value(&pf.name).Validate(nonEmpty),
			huh.NewConfirm().Title("Ephemeral").Description("This preset creates ephemeral JIT slots (else persistent runners).").Value(&pf.ephemeral),
			huh.NewInput().Title("Labels - comma-separated, optional").Placeholder("gpu, large").Value(&pf.labels),
			huh.NewInput().Title("Group id - optional (0 = org default)").Placeholder("0").Value(&pf.groupID).Validate(optionalNonNegInt),
			huh.NewInput().Title("Default container image - metadata, optional").Placeholder("cuda:12").Value(&pf.container),
			huh.NewConfirm().Title("Delete instead").Description("Remove this profile (by name) rather than save it.").Value(&pf.del),
		),
	).WithWidth(66)
	return pf
}

// profile builds the edited core.RunnerProfile (public fields only; saveProfileCmd
// preserves the rest by overlaying onto the existing profile of this name).
func (pf *profileForm) profile() core.RunnerProfile {
	gid, _ := strconv.ParseInt(strings.TrimSpace(pf.groupID), 10, 64)
	return core.RunnerProfile{
		Name:                  strings.TrimSpace(pf.name),
		Labels:                splitComma(pf.labels),
		GroupID:               gid,
		Ephemeral:             pf.ephemeral,
		DefaultContainerImage: strings.TrimSpace(pf.container),
	}
}

// optionalNonNegInt accepts an empty string or a non-negative integer (group id).
func optionalNonNegInt(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if n, err := strconv.Atoi(s); err != nil || n < 0 {
		return fmt.Errorf("must be a non-negative integer (or empty)")
	}
	return nil
}
