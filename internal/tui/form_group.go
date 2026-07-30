package tui

import (
	"strconv"

	huh "charm.land/huh/v2"

	"github.com/erlete/srm/internal/service"
)

// groupForm is the huh form that creates or edits a runner group: a name and a
// visibility (all | selected | private). When visibility is "selected" the Model
// chains into the repo picker so the operator can assign repositories - mirroring
// the GitHub web runner-group flow. A create form also collects the org; an edit
// form fixes the org (and id) of the row being edited.
type groupForm struct {
	form       *huh.Form
	editing    bool
	org        string
	id         int64
	name       string
	visibility string
}

func newGroupForm(orgs []string) *groupForm {
	gf := &groupForm{visibility: "all"}
	if len(orgs) > 0 {
		gf.org = orgs[0]
	}
	gf.form = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().Title("Org").Options(orgOptions(orgs)...).Value(&gf.org),
			huh.NewInput().Title("Group name").Placeholder("e.g. build, ci, ctf").Value(&gf.name).Validate(nonEmpty),
			huh.NewSelect[string]().Title("Visibility").Options(visibilityOptions()...).Value(&gf.visibility),
			huh.NewNote().Description("With \"selected\" you pick which repositories may use this group (next step)."),
		),
	).WithWidth(58)
	return gf
}

func newGroupEditForm(g service.GroupWithOrg) *groupForm {
	gf := &groupForm{
		editing:    true,
		org:        g.Org,
		id:         g.Group.ID,
		name:       g.Group.Name,
		visibility: g.Group.Visibility,
	}
	gf.form = huh.NewForm(
		huh.NewGroup(
			huh.NewNote().Title("Edit group").Description(g.Org+" · id "+strconv.FormatInt(g.Group.ID, 10)),
			huh.NewInput().Title("Group name").Value(&gf.name).Validate(nonEmpty),
			huh.NewSelect[string]().Title("Visibility").Options(visibilityOptions()...).Value(&gf.visibility),
			huh.NewNote().Description("With \"selected\" you manage repository access next."),
		),
	).WithWidth(58)
	return gf
}

func visibilityOptions() []huh.Option[string] {
	return []huh.Option[string]{
		huh.NewOption("all - every repo in the org", "all"),
		huh.NewOption("selected - only chosen repos", "selected"),
		huh.NewOption("private - private repos only", "private"),
	}
}

// opensPicker reports whether the saved group should chain into the repo picker.
func (gf *groupForm) opensPicker() bool { return gf.visibility == "selected" }

// runnerEditForm edits a live persistent runner. GitHub does not expose live label
// editing (go-github v88 has no such call), so this edits the one thing the API
// allows - the runner's group - and points at recreate (c) for label changes. A
// runner belongs to exactly one group.
type runnerEditForm struct {
	form     *huh.Form
	org      string
	runnerID int64
	name     string
	group    string
}

// newRunnerEditForm builds the runner group-edit form as a Select over the org's
// existing groups (loaded async before this is called), so an invalid/nonexistent
// group can't be entered. The runner's current group and "Default" are always
// present in the list.
func newRunnerEditForm(r service.FusedRunner, groups []string) *runnerEditForm {
	rf := &runnerEditForm{org: r.Org, runnerID: r.Runner.ID, name: r.Runner.Name, group: r.GroupName}
	if rf.group == "" {
		rf.group = "Default"
	}
	rf.form = huh.NewForm(
		huh.NewGroup(
			huh.NewNote().Title("Edit runner").Description(r.Runner.Name+"  ·  "+r.Org+"\nLabels can't be edited on a live runner - use recreate (c) to change them."),
			huh.NewSelect[string]().Title("Move to group").Options(groupSelectOptions(groups, rf.group)...).Value(&rf.group),
		),
	).WithWidth(60)
	return rf
}

// groupSelectOptions builds the group Select options: "Default" first, then the
// org's groups, and the runner's current group last if it wasn't already listed -
// deduped and never empty.
func groupSelectOptions(groups []string, current string) []huh.Option[string] {
	seen := map[string]bool{}
	var opts []huh.Option[string]
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		opts = append(opts, huh.NewOption(name, name))
	}
	add("Default")
	for _, g := range groups {
		add(g)
	}
	add(current)
	return opts
}
