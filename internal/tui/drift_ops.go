package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

// previewMsg carries the result of a dry-run plan pass to the preview modal, along
// with the real op to launch on Apply. err means the plan pass itself failed. When
// typedToken is set, the preview's Apply opens a typed-confirm gate (the strongest
// rung) requiring that exact token before the op runs - used for uninstall.
type previewMsg struct {
	title      string
	note       string
	lines      []string
	spec       opSpec
	typedToken string
	err        error
}

// planFixCmd runs a dry-run reconcile-fix pass and builds the preview (the plan)
// plus the apply op. org "" = every configured org (org-WIDE, not single-runner).
func planFixCmd(ctx context.Context, mgr *service.Manager, org string, t Theme) tea.Cmd {
	return func() tea.Msg {
		rep, err := mgr.ReconcileFix(ctx, org, true)
		if err != nil {
			return previewMsg{err: err}
		}
		lines := fixPlanLines(t, rep)
		apply := opSpec{
			Title: "Fix drift",
			Noun:  "runner",
			Run: func(ctx context.Context, mgr *service.Manager, _ chan<- core.ProgressEvent) ([]opItem, error) {
				rep, err := mgr.ReconcileFix(ctx, org, false)
				if err != nil {
					return nil, err
				}
				return fixResultItems(rep), nil
			},
		}
		return previewMsg{title: "Fix drift (preview)", note: scopeNote(org) + " · host-side only; GitHub is never deleted", lines: lines, spec: apply}
	}
}

// planReapCmd runs a dry-run ephemeral-ghost reap and builds the preview + apply op.
func planReapCmd(ctx context.Context, mgr *service.Manager, org string, t Theme) tea.Cmd {
	return func() tea.Msg {
		rep, err := mgr.ReapEphemeral(ctx, org, true)
		if err != nil {
			return previewMsg{err: err}
		}
		lines := reapPlanLines(t, rep)
		apply := opSpec{
			Title: "Reap ephemeral ghosts",
			Noun:  "ghost",
			Run: func(ctx context.Context, mgr *service.Manager, _ chan<- core.ProgressEvent) ([]opItem, error) {
				rep, err := mgr.ReapEphemeral(ctx, org, false)
				if err != nil {
					return nil, err
				}
				return reapResultItems(rep), nil
			},
		}
		note := scopeNote(org) + " · DEREGISTERS these offline ephemeral registrations from GitHub"
		return previewMsg{title: "Reap ephemeral ghosts (preview)", note: note, lines: lines, spec: apply}
	}
}

// fixPlanLines renders the per-runner repair plan from a dry-run report.
func fixPlanLines(t Theme, rep service.ReconcileReport) []string {
	var lines []string
	var busy int
	for _, st := range rep.Runners {
		if st.Fix == "" {
			continue
		}
		if st.Class == service.ClassHealthy {
			continue
		}
		line := t.driftBadge(st.Class) + "  " + t.Crumb.Render(st.Org) + " " + st.Name + t.Faint.Render("  "+st.Fix)
		if st.Busy {
			busy++
		}
		lines = append(lines, line)
	}
	if busy > 0 {
		lines = append(lines, t.Busy.Render(fmt.Sprintf("  %d busy runner(s) will be SKIPPED (a job is running)", busy)))
	}
	return lines
}

// fixResultItems maps an applied reconcile report to op result rows.
func fixResultItems(rep service.ReconcileReport) []opItem {
	var items []opItem
	for _, st := range rep.Runners {
		if st.Fix == "" && st.FixErr == nil {
			continue
		}
		it := opItem{Label: st.Org + "/" + st.Name, Detail: st.Fix}
		switch {
		case st.FixErr != nil:
			it.Outcome, it.Detail = outcomeFail, st.FixErr.Error()
		case st.Fix == "" || startsWith(st.Fix, "skipped"):
			it.Outcome = outcomeSkip
		default:
			it.Outcome = outcomeOK
		}
		items = append(items, it)
	}
	return items
}

// reapPlanLines renders the ghosts a reap would deregister, from a dry-run report.
func reapPlanLines(t Theme, rep service.ReconcileReport) []string {
	var lines []string
	for _, st := range rep.Reaped {
		lines = append(lines, "  "+t.Crumb.Render(st.Org)+" "+st.Name+t.Faint.Render("  ("+st.Fix+")"))
	}
	return lines
}

// reapResultItems maps an applied reap report to op result rows.
func reapResultItems(rep service.ReconcileReport) []opItem {
	var items []opItem
	for _, st := range rep.Reaped {
		it := opItem{Label: st.Org + "/" + st.Name, Detail: st.Fix}
		if st.FixErr != nil {
			it.Outcome, it.Detail = outcomeFail, st.FixErr.Error()
		} else {
			it.Outcome = outcomeOK
		}
		items = append(items, it)
	}
	return items
}

func scopeNote(org string) string {
	if org == "" {
		return "scope: ALL orgs"
	}
	return "scope: " + org
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
