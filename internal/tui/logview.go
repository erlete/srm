package tui

import (
	"bytes"
	"context"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/erlete/srm/internal/service"
)

// logView is the full-screen, scrollable Logs panel (opened with L on a runner or
// ephemeral slot). It shows a snapshot of the host log; when follow is on, the
// model re-polls on a ticker and this view auto-scrolls to the tail. Backed by a
// bubbles viewport, mirroring infoView.
type logView struct {
	vp     viewport.Model
	theme  Theme
	title  string
	follow bool
	ready  bool
}

func newLogView(t Theme) logView {
	return logView{vp: viewport.New(), theme: t}
}

func (v *logView) setSize(w, h int) {
	v.vp.SetWidth(w)
	if h > 1 {
		v.vp.SetHeight(h - 1) // leave a row for the title
	}
	v.ready = true
}

// set replaces the panel content. When following, it keeps the view pinned to the
// tail (newest lines); otherwise it preserves the reader's scroll position.
func (v *logView) set(content string) {
	if content == "" {
		content = v.theme.Faint.Render("(no log output yet)")
	}
	v.vp.SetContent(content)
	if v.follow {
		v.vp.GotoBottom()
	}
}

func (v logView) update(msg tea.Msg) (logView, tea.Cmd) {
	var cmd tea.Cmd
	v.vp, cmd = v.vp.Update(msg)
	return v, cmd
}

func (v logView) view() string {
	mode := v.theme.Faint.Render("snapshot · f to follow · esc to close")
	if v.follow {
		mode = v.theme.Busy.Render("following · f to pause · esc to close")
	}
	head := v.theme.Crumb.Render("Logs") + "  " + v.theme.PanelTtl.Render(v.title) + "  " + mode
	return lipgloss.JoinVertical(lipgloss.Left, head, v.vp.View())
}

// logSubject identifies which runner or slot the Logs panel is showing, so the
// follow ticker can re-fetch the same target across Update cycles.
type logSubject struct {
	org    string
	name   string // persistent runner name (when !isSlot)
	slot   string // ephemeral slot id (when isSlot)
	isSlot bool
}

// logFollowEvery is how often follow mode re-polls the host log.
const logFollowEvery = 2 * time.Second

// logsMsg carries a fetched log snapshot to the Logs panel. subj tags the result
// so a snapshot for a since-changed subject is ignored (the panel switched).
type logsMsg struct {
	subj    logSubject
	content string
	err     error
}

// logTickMsg fires on the follow interval; the handler re-fetches when the panel
// is still open and following.
type logTickMsg struct{}

func logTickCmd() tea.Cmd {
	return tea.Tick(logFollowEvery, func(time.Time) tea.Msg { return logTickMsg{} })
}

// logsCmd fetches a host-log snapshot for the subject into a buffer and returns it
// as a logsMsg. It always reads a snapshot (Follow=false); follow is realized by
// re-polling on the ticker, which keeps the event loop race-free.
func logsCmd(ctx context.Context, mgr *service.Manager, subj logSubject) tea.Cmd {
	return func() tea.Msg {
		var buf bytes.Buffer
		opts := service.LogOptions{Lines: 400}
		var err error
		if subj.isSlot {
			err = mgr.EphemeralLogs(ctx, subj.org, subj.slot, opts, &buf)
		} else {
			err = mgr.RunnerLogs(ctx, subj.org, subj.name, opts, &buf)
		}
		return logsMsg{subj: subj, content: buf.String(), err: err}
	}
}
