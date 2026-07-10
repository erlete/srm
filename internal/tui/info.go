package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

// infoView is the full-screen, scrollable Information panel (opened with i). It
// replaces the old dense bottom detail pane with a roomy, sectioned read-out plus a
// copy-ready "use in a workflow" runs-on snippet. Backed by a bubbles viewport so
// long content scrolls.
type infoView struct {
	vp    viewport.Model
	theme Theme
	title string
	ready bool
}

func newInfoView(t Theme) infoView {
	return infoView{vp: viewport.New(), theme: t}
}

func (v *infoView) setSize(w, h int) {
	v.vp.SetWidth(w)
	if h > 1 {
		v.vp.SetHeight(h - 1) // leave a row for the title
	}
	v.ready = true
}

func (v *infoView) set(title, content string) {
	v.title = title
	v.vp.SetContent(content)
	v.vp.GotoTop()
}

func (v infoView) update(msg tea.Msg) (infoView, tea.Cmd) {
	var cmd tea.Cmd
	v.vp, cmd = v.vp.Update(msg)
	return v, cmd
}

func (v infoView) view() string {
	// A small "Information" label names the screen (per the feedback), with the
	// subject as the title beside it.
	head := v.theme.Crumb.Render("Information") + "  " + v.theme.PanelTtl.Render(v.title)
	return lipgloss.JoinVertical(lipgloss.Left, head, v.vp.View())
}

// --- content builders -------------------------------------------------------
//
// Each returns (title, body). Bodies are themed lipgloss, well-spaced (one section
// per block), and include a runs-on snippet so an operator can copy how to target
// the runner/group from a workflow.

// kvSection renders a titled block of "  key   value" rows.
func kvSection(t Theme, title string, kv [][2]string) string {
	lines := []string{t.PanelTtl.Render(title)}
	for _, p := range kv {
		if p[1] == "" {
			continue
		}
		lines = append(lines, t.Crumb.Render(fmt.Sprintf("  %-11s", p[0]))+p[1])
	}
	return strings.Join(lines, "\n")
}

// yamlBox renders a runs-on snippet inside a bordered code box.
func (t Theme) yamlBox(lines []string) string {
	return t.PanelTtl.Render("Use in a workflow") + "\n" +
		t.Panel.Render(strings.Join(lines, "\n"))
}

func runnerInfo(t Theme, r service.FusedRunner, job *core.RunnerJob, jobState string) (string, string) {
	title := r.Runner.Name + "  ·  " + r.Org

	status := "offline"
	switch {
	case r.Runner.Busy:
		status = "busy (running a job)"
	case r.Runner.Online():
		status = "online (idle)"
	}
	host := "remote (another machine)"
	if r.Local {
		host = fmt.Sprintf("this host  ·  id %d", r.Runner.ID)
	}

	published := orQ(r.Published)
	if r.Behind {
		published += "  (this runner is behind)"
	} else if r.Published != "" {
		published += "  (up to date)"
	}

	mem := "host tier not loaded"
	if r.HostKnown {
		mem = fmt.Sprintf("live %s · peak %s · cap %s", humanBytes(r.MemCur), humanBytes(r.MemPeak), humanBytes(r.MemMax))
	}
	drift := "unaudited (host tier not loaded)"
	if r.DriftClass != "" {
		_, l := driftGlyph(r.DriftClass)
		drift = l
		if r.DriftDetail != "" {
			drift += " - " + r.DriftDetail
		}
	}

	blocks := []string{
		kvSection(t, "Status", [][2]string{
			{"state", status},
			{"host", host},
			{"os", osCell(r.Runner.OS)},
		}),
		kvSection(t, "Agent", [][2]string{
			{"installed", orQ(r.AgentVersion)},
			{"published", published},
			{"previous", r.PreviousVersion},
			{"template", templateLabel(r.TemplateVersion)},
		}),
		kvSection(t, "Drift", [][2]string{{"class", drift}}),
		kvSection(t, "Memory", [][2]string{
			{"cgroup", mem},
			{"oom", oomLabel(r.OOMKills)},
		}),
		kvSection(t, "Timeline", [][2]string{
			{"created", niceTime(r.ConfiguredAt)},
			{"upgraded", niceTime(r.LastUpgradeAt)},
		}),
		kvSection(t, "Targeting", [][2]string{
			{"group", groupCell(r.GroupName, r.Runner.GroupID)},
			{"labels", labelString(r.Runner.Labels)},
		}),
		t.yamlBox(runsOnSnippet(customLabels(r.Runner.Labels), r.GroupName)),
		currentJobBlock(t, r.Runner.Busy, job, jobState),
	}
	return title, strings.Join(blocks, "\n\n")
}

// currentJobBlock renders the "Current job" section from an async lookup. jobState
// is "" (idle/not looked up), "looking", "found", "none", or "error".
func currentJobBlock(t Theme, busy bool, job *core.RunnerJob, jobState string) string {
	var kv [][2]string
	switch {
	case !busy:
		kv = [][2]string{{"status", "idle - no job running"}}
	case jobState == "looking":
		kv = [][2]string{{"status", "running a job - looking it up…"}}
	case jobState == "found" && job != nil:
		kv = [][2]string{
			{"repo", job.Repo},
			{"workflow", job.Workflow},
			{"job", job.JobName},
			{"url", job.URL},
		}
	case jobState == "error":
		kv = [][2]string{{"status", "running a job (lookup failed - the App may lack Actions:Read on the repo)"}}
	default: // "none"
		kv = [][2]string{{"status", "running a job, not found in repos this App can read (grant Actions:Read to see it)"}}
	}
	return kvSection(t, "Current job", kv)
}

// healthInfo renders the Information panel for a Health org card: the per-org
// doctor read-out (auth, retention, agent freshness) plus a short host context.
func healthInfo(t Theme, rep healthReport, host healthHost) (string, string) {
	title := rep.Org

	auth := "ok"
	if rep.AuthErr != nil {
		auth = "error - " + rep.AuthErr.Error()
	}
	retention := fmt.Sprintf("%d days (max %d)", rep.RetentionDays, rep.RetentionMax)
	if rep.RetentionErr != nil {
		retention = "unavailable - " + rep.RetentionErr.Error()
	}
	agent := "unknown (host tier not loaded)"
	if rep.AgentOK {
		agent = "published " + orQ(rep.AgentCurrent)
		if rep.AgentBehind > 0 {
			agent += fmt.Sprintf("  ·  %d recorded runner(s) behind (u to upgrade)", rep.AgentBehind)
		} else {
			agent += "  ·  all recorded runners up to date"
		}
	}

	// Host toolchain summary (present vs missing), from the shared host doctor block.
	var missing []string
	for _, tp := range host.Tools {
		if !tp.Found {
			missing = append(missing, tp.Name)
		}
	}
	tools := fmt.Sprintf("%d present", len(host.Tools)-len(missing))
	if len(missing) > 0 {
		tools += "  ·  missing: " + strings.Join(missing, ", ") + " (P to provision)"
	}

	blocks := []string{
		kvSection(t, "Org", [][2]string{
			{"org", rep.Org},
			{"runners", fmt.Sprintf("%d (%d online)", rep.Runners, rep.Online)},
			{"auth", auth},
		}),
		kvSection(t, "Retention", [][2]string{{"artifacts/logs", retention}}),
		kvSection(t, "Agent", [][2]string{{"status", agent}}),
		kvSection(t, "Host", [][2]string{
			{"capacity", orQ(host.CapacityMode)},
			{"toolchain", tools},
		}),
		t.Faint.Render("Host-wide capacity, disks, and caches live on the Health tab's host panel."),
	}
	return title, strings.Join(blocks, "\n\n")
}

func slotInfo(t Theme, s service.FusedSlot) (string, string) {
	title := "slot " + s.Slot + "  ·  " + s.Org
	label, _ := slotState(s.EphemeralSlot)

	mem := fmt.Sprintf("live %s · peak %s · cap %s", humanBytes(s.MemCur), humanBytes(s.MemPeak), humanBytes(s.MemMax))
	blocks := []string{
		kvSection(t, "Status", [][2]string{
			{"state", label},
			{"restarts", fmt.Sprintf("%d", s.Restarts)},
			{"unit", okDrift(s.UnitOK)},
		}),
		kvSection(t, "Agent", [][2]string{
			{"installed", orQ(s.AgentVersion)},
			{"published", orQ(s.Published)},
		}),
		kvSection(t, "Memory", [][2]string{
			{"cgroup", mem},
			{"oom", oomLabel(s.OOMKills)},
		}),
		kvSection(t, "Targeting", [][2]string{
			{"group", groupCell(s.GroupName, s.GroupID)},
			{"labels", strings.Join(s.Labels, ", ")},
		}),
		t.yamlBox(runsOnSnippet(s.Labels, s.GroupName)),
		t.Faint.Render("Ephemeral lanes mint a fresh single-use JIT registration per job."),
	}
	return title, strings.Join(blocks, "\n\n")
}

func groupInfo(t Theme, g service.GroupWithOrg, runners []service.FusedRunner, slots []service.FusedSlot, repos []core.Repo, reposState string) (string, string) {
	gr := g.Group
	title := gr.Name + "  ·  " + g.Org
	blocks := []string{
		kvSection(t, "Group", [][2]string{
			{"id", fmt.Sprintf("%d", gr.ID)},
			{"visibility", gr.Visibility},
			{"default", yesNo(gr.Default)},
			{"public", yesNo(gr.AllowsPublic)},
		}),
	}
	// Runners currently in this group (from the loaded fleet snapshot).
	var members []string
	for _, r := range runners {
		if r.GroupName == gr.Name {
			members = append(members, "  "+r.Org+"/"+r.Runner.Name+t.Faint.Render("  "+orQ(r.AgentVersion)))
		}
	}
	for _, s := range slots {
		if s.GroupName == gr.Name {
			members = append(members, "  "+s.Org+"/slot "+s.Slot+t.Faint.Render("  ephemeral"))
		}
	}
	if len(members) == 0 {
		members = []string{t.Faint.Render("  (no runners from the loaded fleet are in this group)")}
	}
	blocks = append(blocks, t.PanelTtl.Render("Runners in this group")+"\n"+strings.Join(members, "\n"))

	blocks = append(blocks, groupReposBlock(t, gr.Visibility, repos, reposState))
	blocks = append(blocks, t.yamlBox([]string{"runs-on:", "  group: " + gr.Name}))
	return title, strings.Join(blocks, "\n\n")
}

// groupReposBlock renders the Repository access section. For "selected" visibility
// it lists the group's assigned repos (loaded async via the runner-group API, which
// returns them regardless of the App's own repo access). reposState is "" / "looking"
// / "loaded".
func groupReposBlock(t Theme, visibility string, repos []core.Repo, reposState string) string {
	lines := []string{t.PanelTtl.Render("Repository access")}
	switch visibility {
	case "all":
		lines = append(lines, t.Crumb.Render("  scope      ")+"every repo in the org")
	case "private":
		lines = append(lines, t.Crumb.Render("  scope      ")+"all private repos in the org")
	default: // selected
		lines = append(lines, t.Crumb.Render("  scope      ")+"selected repos (edit with enter on the Groups tab)")
		switch reposState {
		case "looking":
			lines = append(lines, t.Faint.Render("  loading assigned repos…"))
		case "loaded":
			if len(repos) == 0 {
				lines = append(lines, t.Faint.Render("  (no repos assigned yet)"))
			}
			for _, r := range repos {
				lines = append(lines, "  "+r.FullName)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// runsOnSnippet builds the YAML lines showing both ways to target a runner.
func runsOnSnippet(labels []string, group string) []string {
	all := append([]string{"self-hosted"}, labels...)
	lines := []string{"runs-on: [" + strings.Join(all, ", ") + "]"}
	if group != "" {
		lines = append(lines, "# or, to target its group:", "# runs-on:", "#   group: "+group)
	}
	return lines
}

func templateLabel(v int) string {
	if v <= 0 {
		return ""
	}
	return fmt.Sprintf("v%d", v)
}

func oomLabel(n int64) string {
	if n < 0 {
		return "unknown"
	}
	return fmt.Sprintf("%d kill(s)", n)
}

// niceTime trims an RFC3339 stamp to "YYYY-MM-DD HH:MM" (empty stays empty).
func niceTime(s string) string {
	if s == "" {
		return ""
	}
	s = strings.Replace(s, "T", " ", 1)
	if i := strings.IndexByte(s, ' '); i >= 0 && len(s) >= i+6 {
		return s[:i+6] // date + HH:MM
	}
	return s
}
