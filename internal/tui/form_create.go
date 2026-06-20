package tui

import (
	"fmt"
	"strconv"
	"strings"

	huh "charm.land/huh/v2"

	"github.com/erlete/srm/internal/service"
)

// createForm wraps a huh form for provisioning runners on this host. The same
// struct backs both natures, distinguished by ephemeral: a persistent form
// collects a name prefix (names become <prefix>-N), an ephemeral form omits it
// (slots are numbered lanes 1..N that mint a fresh JIT registration per job).
type createForm struct {
	form      *huh.Form
	ephemeral bool
	org       string
	prefix    string
	count     string
	labels    string
	group     string
}

func newCreateForm(orgs []string) *createForm {
	cf := &createForm{count: "1", prefix: "runner"}
	if len(orgs) > 0 {
		cf.org = orgs[0]
	}
	cf.form = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().Title("Org").Options(orgOptions(orgs)...).Value(&cf.org),
			huh.NewInput().Title("Name prefix").Placeholder("temporal").Value(&cf.prefix).Validate(nonEmpty),
			huh.NewInput().Title("Count").Value(&cf.count).Validate(posInt),
			huh.NewInput().Title("Labels - comma-separated, optional").Placeholder("temporal").Value(&cf.labels),
			huh.NewInput().Title("Group - optional, created if missing").Placeholder("temporal").Value(&cf.group),
		),
	).WithWidth(54)
	return cf
}

// newEphemeralForm builds the create wizard for ephemeral slot lanes. No name
// prefix is collected - slots are numbered 1..N - and the help text names them
// plainly so the ephemeral nature is never mistaken for a persistent runner.
func newEphemeralForm(orgs []string) *createForm {
	cf := &createForm{ephemeral: true, count: "1"}
	if len(orgs) > 0 {
		cf.org = orgs[0]
	}
	cf.form = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().Title("Org").Options(orgOptions(orgs)...).Value(&cf.org),
			huh.NewNote().Description("Ephemeral slots are numbered lanes (1..N). Each mints a single-use JIT registration per job and runs on a clean slate."),
			huh.NewInput().Title("Slot count").Value(&cf.count).Validate(posInt),
			huh.NewInput().Title("Labels - comma-separated, optional").Placeholder("temporal").Value(&cf.labels),
			huh.NewInput().Title("Group - optional, created if missing").Placeholder("temporal").Value(&cf.group),
		),
	).WithWidth(58)
	return cf
}

func orgOptions(orgs []string) []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(orgs))
	for _, o := range orgs {
		opts = append(opts, huh.NewOption(o, o))
	}
	return opts
}

// spec turns the collected values into a DeploySpec.
func (cf *createForm) spec() service.DeploySpec {
	n, _ := strconv.Atoi(strings.TrimSpace(cf.count))
	if n < 1 {
		n = 1
	}
	return service.DeploySpec{
		Org:        cf.org,
		Count:      n,
		NamePrefix: strings.TrimSpace(cf.prefix),
		Labels:     splitComma(cf.labels),
		Group:      strings.TrimSpace(cf.group),
	}
}

func nonEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("required")
	}
	return nil
}

func posInt(s string) error {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err != nil || n < 1 {
		return fmt.Errorf("must be a positive integer")
	}
	return nil
}

func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
