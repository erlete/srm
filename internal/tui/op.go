package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

// The operation runner generalizes the create flow's progress pipeline into a
// reusable mechanism for every long-running fleet op (refresh, upgrade, rollback,
// prune, provision, recreate, reconcile-fix, reap, and their bulk variants). An
// op streams progress over a channel (true per-item streaming for ops that take a
// channel, like create/upgrade; an indeterminate spinner for ops that return all
// at once) and ends in a per-item RESULT PANEL: a glyph + label + detail per
// subject, plus an aggregate tally. It honors the singleton modal invariant - a
// confirm/preview/typed-confirm closes BEFORE the op panel opens, never stacked.

// opOutcome classifies one subject's result in the op result panel.
type opOutcome int

const (
	outcomeOK       opOutcome = iota // applied/created/upgraded
	outcomeSkip                      // intentionally not touched (busy, at-target, dry-run, a gate)
	outcomeRollback                  // attempted, failed self-test, restored to the prior state
	outcomeFail                      // errored
)

// opItem is one subject's result row in the op result panel.
type opItem struct {
	Label   string
	Outcome opOutcome
	Detail  string // "from -> to", a skip reason, or an error string
}

// opResult is an op's final outcome: the per-item rows and any fatal error.
type opResult struct {
	items []opItem
	err   error
}

// opSpec describes a long-running fleet operation. Run does the work, forwarding
// per-item progress over the channel when it can stream and returning the final
// per-item results. It runs in a goroutine; it MUST NOT touch the Model.
type opSpec struct {
	Title string
	Noun  string
	Run   func(ctx context.Context, mgr *service.Manager, progress chan<- core.ProgressEvent) ([]opItem, error)
}

// opState is the live op the Model is running or showing results for. open while
// the panel is visible (running, then showing the result list until dismissed).
type opState struct {
	open    bool
	running bool
	title   string
	noun    string
	ch      chan core.ProgressEvent
	resCh   chan opResult
	done    int
	total   int
	msg     string
	items   []opItem
	err     error
}

// startOp launches spec.Run in a goroutine and returns the live op state. The
// caller batches waitOpCmd(ch, resCh) and the spinner tick to drive it.
func startOp(ctx context.Context, mgr *service.Manager, spec opSpec) opState {
	ch := make(chan core.ProgressEvent)
	resCh := make(chan opResult, 1)
	go func() {
		items, err := spec.Run(ctx, mgr, ch)
		close(ch)
		resCh <- opResult{items: items, err: err}
	}()
	return opState{open: true, running: true, title: spec.Title, noun: spec.Noun, ch: ch, resCh: resCh, msg: "working…"}
}

// waitOpCmd blocks for the next progress event; when the stream closes it reads
// the final result and emits opDoneMsg. Mirrors waitCreateCmd.
func waitOpCmd(ch chan core.ProgressEvent, resCh chan opResult) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			res := <-resCh
			return opDoneMsg{items: res.items, err: res.err}
		}
		return opProgressMsg{ev: ev}
	}
}

// renderOpPanel draws the op modal: a live spinner + progress + current message
// while running, then a per-item outcome list + tally + dismiss hint when done.
func (m Model) renderOpPanel(width, height int) string {
	t := m.theme
	op := m.op
	inner := width - 10
	if inner > 100 {
		inner = 100
	}
	if inner < 24 {
		inner = 24
	}
	var rows []string
	rows = append(rows, t.ModalT.Render(op.title))
	rows = append(rows, "")

	if op.running {
		if op.total > 0 {
			rows = append(rows, m.prog.ViewAs(float64(op.done)/float64(op.total)))
			rows = append(rows, "")
			rows = append(rows, clip(t.Help.Render(fmt.Sprintf("%s  %d/%d - %s", m.spin.View(), op.done, op.total, op.msg)), inner))
		} else {
			rows = append(rows, clip(t.Help.Render(m.spin.View()+"  "+op.msg), inner))
		}
	} else {
		if op.err != nil {
			rows = append(rows, clip(t.StatusErr.Render("✗ "+op.err.Error()), inner))
			rows = append(rows, "")
		}
		rows = append(rows, m.opItemLines(op.items, inner)...)
		rows = append(rows, "")
		rows = append(rows, t.Faint.Render(opTally(op.items)))
		rows = append(rows, t.Help.Render("enter/esc to dismiss"))
	}

	box := t.Modal.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// opItemLines renders the per-item outcome rows (glyph + label + detail), capping
// the visible count and clipping each line to inner width so a fleet-wide op never
// overflows the panel.
func (m Model) opItemLines(items []opItem, inner int) []string {
	t := m.theme
	const limit = 16
	out := make([]string, 0, len(items)+1)
	for i, it := range items {
		if i >= limit {
			out = append(out, t.Faint.Render(fmt.Sprintf("  … and %d more", len(items)-limit)))
			break
		}
		glyph, style := opGlyph(t, it.Outcome)
		line := style.Render(glyph) + " " + it.Label
		if it.Detail != "" {
			line += t.Faint.Render("  " + it.Detail)
		}
		out = append(out, clip(line, inner))
	}
	if len(items) == 0 {
		out = append(out, t.Faint.Render("  (nothing to do)"))
	}
	return out
}

// opGlyph maps an outcome to its glyph + color.
func opGlyph(t Theme, o opOutcome) (string, lipgloss.Style) {
	switch o {
	case outcomeOK:
		return "✓", t.Online
	case outcomeSkip:
		return "•", t.Faint
	case outcomeRollback:
		return "↩", t.Busy
	default:
		return "✗", t.Offline
	}
}

// opTally summarizes the per-item outcomes for the panel footer.
func opTally(items []opItem) string {
	var ok, skip, rb, fail int
	for _, it := range items {
		switch it.Outcome {
		case outcomeOK:
			ok++
		case outcomeSkip:
			skip++
		case outcomeRollback:
			rb++
		default:
			fail++
		}
	}
	var parts []string
	add := func(n int, label string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	add(ok, "ok")
	add(skip, "skipped")
	add(rb, "rolled back")
	add(fail, "failed")
	if len(parts) == 0 {
		return "no items"
	}
	return strings.Join(parts, " · ")
}
