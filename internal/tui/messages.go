package tui

import "github.com/erlete/srm/internal/service"

// Typed messages flowing through the Bubble Tea event loop. Every GitHub call
// runs inside a tea.Cmd (see commands.go) and emits one of these. A non-nil err
// is surfaced in the status bar; partial data (e.g. one org failing) still
// renders with a warning.

type runnersMsg struct {
	rows []service.RunnerWithOrg
	err  error
}

type groupsMsg struct {
	rows []service.GroupWithOrg
	err  error
}

type ephemeralMsg struct {
	rows []service.EphemeralSlot
	err  error
}

type healthMsg struct {
	reports []healthReport
	err     error
}

// settingsMsg carries the resolved capacity-policy snapshot to the Settings view.
type settingsMsg struct {
	snap settingsSnapshot
}

// actionMsg reports the outcome of a mutation (delete/destroy/create).
type actionMsg struct {
	summary string
	err     error
}
