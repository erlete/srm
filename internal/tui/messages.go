package tui

import (
	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

// Typed messages flowing through the Bubble Tea event loop. Every GitHub call
// runs inside a tea.Cmd (see commands.go) and emits one of these. A non-nil err
// is surfaced in the status bar; partial data (e.g. one org failing) still
// renders with a warning.

type groupsMsg struct {
	rows []service.GroupWithOrg
	err  error
}

type healthMsg struct {
	reports []healthReport
	host    healthHost
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

// fleetMsg carries the fast-tier fused snapshot (GitHub list + manifest versions +
// published comparison) to the Persistent / Ephemeral / Drift views. The host tier
// follows asynchronously via hostTierMsg.
type fleetMsg struct {
	snap service.FleetSnapshot
}

// hostTierMsg carries a reconcile pass for the slow/host tier. On success it is
// merged into the live snapshot (drift class, cgroup memory, host health); on
// error the host tier is marked unavailable and the fast data still renders.
// orgFilter tags the scope so a result for a since-changed filter is ignored.
type hostTierMsg struct {
	rep       service.ReconcileReport
	orgFilter string
	err       error
}

// autoTickMsg fires on the auto-refresh interval. The handler reloads only when
// the idle guard passes (no modal/form/op/filter and not already loading), then
// re-arms the ticker.
type autoTickMsg struct{}

// opProgressMsg carries one event from a running operation to the op result panel.
type opProgressMsg struct{ ev core.ProgressEvent }

// opDoneMsg is the final outcome of an operation: per-item results + any fatal error.
type opDoneMsg struct {
	items []opItem
	err   error
}
