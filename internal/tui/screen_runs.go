package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/joblog"
	"github.com/erlete/srm/internal/service"
)

// runsView is the Runs tab: a compact live "active now" block (the jobs the fleet's
// busy runners are running right now, joined from GitHub) above a host-local table of
// captured ephemeral job runs from the durable joblog store, which outlives the
// per-cycle _diag wipe. The table is READ-ONLY (runs are history, not entities you
// mutate): L opens a run's captured log, i shows its detail. The unique value is the
// durable run history + logs that nothing else keeps, plus the live active join.
type runsView struct {
	st     scrollTable
	cols   []table.Column
	all    []joblog.Meta
	rows   []joblog.Meta
	theme  Theme
	width  int
	height int
	flt    filterState
	err    error

	// "active now" (live from GitHub): the jobs the fleet's busy runners are running
	// right now, shown as a compact block above the durable history table.
	active    []service.ActiveRun
	activeErr error
}

// activeMax caps how many active runs the compact block lists (the rest collapse to
// a "+N more" line) so the block never crowds out the history table.
const activeMax = 6

func newRunsView(t Theme) runsView {
	cols := []table.Column{
		{Title: "RUNNER", Width: 26},
		{Title: "ORG", Width: 16},
		{Title: "SLOT", Width: 6},
		{Title: "RESULT", Width: 7},
		{Title: "STARTED", Width: 19},
		{Title: "DURATION", Width: 9},
		{Title: "LOG", Width: 4},
	}
	return runsView{st: newScrollTable(cols, t.Table), cols: cols, theme: t,
		flt: newFilterState("type to filter by runner / org / slot / result…")}
}

func (v *runsView) setSize(w, h int) {
	v.width, v.height = w, h
	v.st.setWidth(w)
	v.st.setColumns(fillWidth(v.cols, w))
	v.flt.setWidth(w - 4)
	// The history table gets whatever height the "active now" block leaves it.
	th := h - v.activeHeight()
	if th > 3 {
		v.st.setHeight(th)
	}
}

// setData replaces the run history (already org-filtered by the model). err carries a
// store-read failure (e.g. a non-elevated session on the root-owned store).
func (v *runsView) setData(history []joblog.Meta, err error) {
	v.all = history
	v.err = err
	v.applyFilter()
}

// setActive replaces the "active now" block data (already org-filtered by the model),
// then re-applies the size so the history table height accounts for the block.
func (v *runsView) setActive(runs []service.ActiveRun, err error) {
	v.active = runs
	v.activeErr = err
	v.setSize(v.width, v.height)
}

// activeHeight is the rendered height the "active now" block reserves from the history
// table (0 when there is nothing to show).
func (v runsView) activeHeight() int {
	b := v.activeBlock()
	if b == "" {
		return 0
	}
	return strings.Count(b, "\n") + 2 // block lines + a trailing spacer
}

// activeBlock renders the compact "active now" section: the jobs the fleet's busy
// runners are running right now (capped at activeMax; the rest collapse to "+N more").
// Empty (0 height) when nothing is active and no error, so a quiet fleet's history is
// never crowded.
func (v runsView) activeBlock() string {
	t := v.theme
	if v.activeErr != nil {
		return t.Offline.Render("▶ active now: unavailable (" + v.activeErr.Error() + ")")
	}
	if len(v.active) == 0 {
		return ""
	}
	lines := []string{t.Title.Render(fmt.Sprintf("▶ active now (%d)", len(v.active)))}
	for i, r := range v.active {
		if i >= activeMax {
			lines = append(lines, t.Faint.Render(fmt.Sprintf("  +%d more", len(v.active)-activeMax)))
			break
		}
		who := r.RunnerName
		if r.Ephemeral {
			who = "eph " + r.Slot + " · " + r.RunnerName
		}
		lines = append(lines, t.Crumb.Render("  "+r.Org+" "+who)+t.Faint.Render("  →  ")+
			t.Online.Render(r.Job.Repo)+t.Faint.Render(" · "+r.Job.Workflow+" · "+r.Job.JobName))
	}
	return strings.Join(lines, "\n")
}

func (v *runsView) applyFilter() {
	cursor := v.st.cursor()
	q := v.flt.query()
	v.rows = v.rows[:0]
	for _, m := range v.all {
		if q == "" || strings.Contains(v.haystack(m), q) {
			v.rows = append(v.rows, m)
		}
	}
	tr := make([]table.Row, 0, len(v.rows))
	for _, m := range v.rows {
		tr = append(tr, table.Row{
			m.RunnerName, m.Org, m.Slot, runResultCell(m), runStartedStr(m), runDurationStr(m), yesDash(m.HasLog),
		})
	}
	v.st.setRows(tr)
	if cursor >= len(tr) {
		cursor = len(tr) - 1
	}
	if cursor >= 0 {
		v.st.setCursor(cursor)
	}
}

func (v runsView) haystack(m joblog.Meta) string {
	res := "ok"
	if !m.OK {
		res = "fail"
	}
	return strings.ToLower(strings.Join([]string{m.RunnerName, m.Org, m.Slot, res}, " "))
}

func (v *runsView) startFilter() tea.Cmd { return v.flt.start() }
func (v *runsView) stopFilter(clear bool) {
	v.flt.stop(clear)
	if clear {
		v.applyFilter()
	}
}
func (v runsView) filtering() bool { return v.flt.filtering() }

func (v runsView) updateFilter(msg tea.Msg) (runsView, tea.Cmd) {
	var cmd tea.Cmd
	v.flt, cmd = v.flt.update(msg)
	v.applyFilter()
	return v, cmd
}

func (v runsView) update(msg tea.Msg) (runsView, tea.Cmd) {
	cmd := v.st.update(msg)
	return v, cmd
}

func (v runsView) selected() (joblog.Meta, bool) {
	i := v.st.cursor()
	if i < 0 || i >= len(v.rows) {
		return joblog.Meta{}, false
	}
	return v.rows[i], true
}

func (v runsView) tableView() string { return v.st.view() }

func (v runsView) bottomLine() string {
	if v.flt.shown() {
		return v.flt.line(v.theme)
	}
	if v.err != nil {
		return v.theme.Offline.Render("cannot read the job-log store: " + v.err.Error())
	}
	if len(v.all) == 0 {
		return v.theme.Help.Render("no captured runs yet - ephemeral jobs are recorded here as they finish (run elevated on the host)")
	}
	m, ok := v.selected()
	if !ok {
		return v.theme.Help.Render("no run selected")
	}
	parts := []string{
		v.theme.Title.Render(m.RunnerName),
		v.theme.Crumb.Render(m.Org + " slot " + m.Slot),
		runResultBadge(v.theme, m),
	}
	if d := m.Duration(); d > 0 {
		parts = append(parts, v.theme.Faint.Render("took "+d.Round(time.Second).String()))
	}
	if m.HasLog {
		parts = append(parts, v.theme.Faint.Render("L to view log"))
	}
	if m.Error != "" {
		parts = append(parts, v.theme.Offline.Render(m.Error))
	}
	return strings.Join(parts, v.theme.Faint.Render(" · "))
}

// runInfo renders the Information panel for a run (the i screen on the Runs tab).
func runInfo(t Theme, m joblog.Meta) (string, string) {
	kv := func(k, val string) string { return t.Crumb.Render(fmt.Sprintf("%-12s", k)) + val }
	res := t.Online.Render("ok")
	if !m.OK {
		res = t.Offline.Render("FAIL")
	}
	lines := []string{
		kv("runner", m.RunnerName),
		kv("runner id", fmt.Sprintf("%d", m.RunnerID)),
		kv("org", m.Org),
		kv("slot", m.Slot),
		kv("result", res),
		kv("started", runStartedStr(m)),
		kv("duration", runDurationStr(m)),
		kv("log", yesDash(m.HasLog)),
	}
	if m.Error != "" {
		lines = append(lines, kv("error", t.Offline.Render(m.Error)))
	}
	lines = append(lines, "", t.Faint.Render("a running job's repo/workflow shows in the 'active now' block above."))
	if m.HasLog {
		lines = append(lines, t.Faint.Render("Press L to view the captured log."))
	}
	return fmt.Sprintf("Run (runner %d)", m.RunnerID), strings.Join(lines, "\n")
}

// runsMsg carries the loaded durable job-log history to the Runs tab.
type runsMsg struct {
	history []joblog.Meta
	err     error
}

// loadRunsCmd reads the durable ephemeral job-log store off the event loop. The store
// is root-owned, so on a non-elevated session List returns a permission error the view
// surfaces (rather than a misleading empty).
func loadRunsCmd(mgr *service.Manager) tea.Cmd {
	return func() tea.Msg {
		h, err := mgr.JobLogStore().List()
		return runsMsg{history: h, err: err}
	}
}

// activeRunsMsg carries the live "active now" join to the Runs tab.
type activeRunsMsg struct {
	runs []service.ActiveRun
	err  error
}

// loadActiveRunsCmd resolves the fleet's currently-running jobs off the event loop (a
// GitHub join scoped to org). Best-effort: an error is surfaced in the block; the
// history table stands on its own regardless.
func loadActiveRunsCmd(mgr *service.Manager, org string) tea.Cmd {
	return func() tea.Msg {
		runs, err := mgr.ActiveRuns(context.Background(), org)
		return activeRunsMsg{runs: runs, err: err}
	}
}

func runResultCell(m joblog.Meta) string {
	if m.OK {
		return "ok"
	}
	return "FAIL"
}

func runStartedStr(m joblog.Meta) string {
	if m.StartedUnix == 0 {
		return "-"
	}
	return m.Started().Format("2006-01-02 15:04:05")
}

func runDurationStr(m joblog.Meta) string {
	if d := m.Duration(); d > 0 {
		return d.Round(time.Second).String()
	}
	return "-"
}

func yesDash(b bool) string {
	if b {
		return "yes"
	}
	return "-"
}

func runResultBadge(t Theme, m joblog.Meta) string {
	if m.OK {
		return t.Online.Render("● ok")
	}
	return t.Offline.Render("✗ FAIL")
}
