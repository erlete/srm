package tui

import (
	"fmt"
	"strconv"
	"strings"

	huh "charm.land/huh/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/service"
)

// settingsSnapshot is the resolved capacity policy shown in the Settings panel.
type settingsSnapshot struct {
	Mode      string                // "" or config.ResourceModeAuto
	Effective config.ResourceLimits // host-level effective caps (ResourcesFor(""))
	Overrides config.ResourceLimits // literal host-wide overrides (cfg.Resources)
	SliceMax  string                // effective aggregate slice ceiling
	SliceSet  bool                  // slice ceiling explicitly overridden
	PerOrg    bool                  // at least one org carries its own resource overrides
	Path      string                // config file changes are written to
}

// settingsView renders the capacity policy as a read-only card. Editing happens in
// settingsForm (opened with 'e'); this is the only panel that mutates config.
type settingsView struct {
	theme Theme
	snap  settingsSnapshot
	width int
}

func newSettingsView(t Theme) settingsView { return settingsView{theme: t} }

func (v *settingsView) setSize(w, _ int) { v.width = w }

func (v *settingsView) setSnapshot(s settingsSnapshot) { v.snap = s }

// card renders the capacity-policy panel at the given width (the LEFT half of the
// Settings split). The Lifecycle menu is the right half (see Model.settingsBody).
func (v settingsView) card(w int) string {
	t := v.theme
	s := v.snap

	kv := func(k, val string) string {
		return t.Crumb.Render(fmt.Sprintf("  %-16s", k)) + val
	}
	orDash := func(val string) string {
		if strings.TrimSpace(val) == "" {
			return t.Faint.Render("(unset)")
		}
		return val
	}

	lines := []string{
		t.PanelTtl.Render("Capacity policy"),
		kv("mode", v.modeLabel()),
		"",
	}
	// The caps panel differs by OS: Linux shows the per-runner cgroup directives plus
	// the aggregate slice; Windows shows the Job-Object subset (MemoryMax + TasksMax,
	// ephemeral jobs only). See capLines in screen_settings_{linux,windows}.go.
	lines = append(lines, v.capLines(kv, orDash)...)

	lines = append(lines, "", kv("config file", t.Faint.Render(s.Path)))
	if s.PerOrg {
		lines = append(lines, kv("note", t.Busy.Render("some orgs have per-org overrides (edit YAML directly)")))
	}

	inner := w - 4
	if inner < 36 {
		inner = 36
	}
	return t.Panel.Width(inner).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}

func (v settingsView) modeLabel() string {
	t := v.theme
	switch {
	case v.snap.Mode == config.ResourceModeAuto:
		return t.Online.Render("auto") + t.Crumb.Render(" - machine-relative %, scales with host RAM, no reconfig on resize")
	case !v.snap.Effective.IsZero():
		return t.StatusInfo.Render("manual") + t.Crumb.Render(" - literal caps below")
	default:
		return t.Offline.Render("off") + t.Crumb.Render(offModeHint)
	}
}

// settingsForm is the huh form that edits the capacity policy. It is a deliberately
// separate concern from the runner-create forms - it writes config, never runners.
type settingsForm struct {
	form     *huh.Form
	mode     string
	memHigh  string
	memMax   string
	memSwap  string
	cpu      string
	tasks    string
	sliceMax string
}

func newSettingsForm(cur service.ResourceSettings) *settingsForm {
	sf := &settingsForm{
		mode:     cur.Mode,
		memHigh:  cur.Resources.MemoryHigh,
		memMax:   cur.Resources.MemoryMax,
		memSwap:  cur.Resources.MemorySwapMax,
		cpu:      cur.Resources.CPUWeight,
		tasks:    cur.Resources.TasksMax,
		sliceMax: cur.SliceMax,
	}
	auto := config.AutoResourceLimits()
	sf.form = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().Title("Capacity mode").Options(
				huh.NewOption("Auto - machine-relative %, scales with host RAM", config.ResourceModeAuto),
				huh.NewOption("Manual / off - literal values (empty field = unbounded)", ""),
			).Value(&sf.mode),
			huh.NewNote().Description(capNoteDesc),
			huh.NewInput().Title("Per-runner MemoryHigh (soft cap)").Placeholder(auto.MemoryHigh).Value(&sf.memHigh).Validate(systemdMemValue),
			huh.NewInput().Title("Per-runner MemoryMax (hard cap)").Placeholder(auto.MemoryMax).Value(&sf.memMax).Validate(systemdMemValue),
			huh.NewInput().Title("Per-runner MemorySwapMax (0 = no swap)").Placeholder(auto.MemorySwapMax).Value(&sf.memSwap).Validate(systemdMemValue),
			huh.NewInput().Title("Aggregate srm.slice MemoryMax (all runners; Auto mode)").Placeholder(config.AutoSliceMemoryMax).Value(&sf.sliceMax).Validate(systemdMemValue),
			huh.NewInput().Title("CPUWeight - optional (1..10000)").Value(&sf.cpu).Validate(optInt),
			huh.NewInput().Title("TasksMax - optional").Value(&sf.tasks).Validate(optInt),
		),
	).WithWidth(66)
	return sf
}

// settings turns the collected values into a ResourceSettings.
func (sf *settingsForm) settings() service.ResourceSettings {
	return service.ResourceSettings{
		Mode: strings.TrimSpace(sf.mode),
		Resources: config.ResourceLimits{
			MemoryHigh:    strings.TrimSpace(sf.memHigh),
			MemoryMax:     strings.TrimSpace(sf.memMax),
			MemorySwapMax: strings.TrimSpace(sf.memSwap),
			CPUWeight:     strings.TrimSpace(sf.cpu),
			TasksMax:      strings.TrimSpace(sf.tasks),
		},
		SliceMax: strings.TrimSpace(sf.sliceMax),
	}
}

// systemdMemValue permissively validates a systemd memory directive: empty,
// "infinity", a percentage ("25%"), or a size/byte value starting with a digit
// ("2G", "0", "1073741824"). systemd is the final arbiter - this only catches typos.
func systemdMemValue(s string) error {
	s = strings.TrimSpace(s)
	if s == "" || s == "infinity" {
		return nil
	}
	s = strings.TrimSuffix(s, "%")
	if s == "" || s[0] < '0' || s[0] > '9' {
		return fmt.Errorf("use a percentage (25%%), size (2G), 0, or infinity")
	}
	return nil
}

// optInt validates an optional positive integer (empty allowed).
func optInt(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if _, err := strconv.Atoi(s); err != nil {
		return fmt.Errorf("must be a whole number or empty")
	}
	return nil
}
