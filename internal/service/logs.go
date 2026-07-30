package service

import (
	"context"
	"fmt"
	"io"

	"github.com/erlete/srm/internal/runner"
)

// LogOptions parameterizes a runner/slot log read (tail length, follow, source).
// It re-exports the runner-package type so CLI/TUI callers stay decoupled from
// the host layer.
type LogOptions = runner.LogOptions

// Log source selectors (re-exported): the systemd journal (default) vs the agent
// _diag logs.
const (
	LogSourceJournal = runner.LogSourceJournal
	LogSourceAgent   = runner.LogSourceAgent
)

// RunnerLogs streams a persistent runner's host log to w. Logs live on the
// runner's OWN machine, so the runner must be installed on THIS host by srm; a
// remote (or not-locally-installed) runner yields a clear error rather than an
// empty read. Reading a system unit's journal needs root/elevation - journalctl's
// own permission error surfaces if the session is not elevated.
func (m *Manager) RunnerLogs(ctx context.Context, org, name string, opts LogOptions, w io.Writer) error {
	org, err := m.requireOrg(org)
	if err != nil {
		return err
	}
	if !m.RunnerIsLocal(org, name) {
		return fmt.Errorf("runner %q is not installed on this host by srm; logs are only available on the runner's own machine", name)
	}
	return m.orchestratorFor(org).RunnerLog(ctx, org, name, opts, w)
}

// EphemeralLogs streams an ephemeral slot lane's host log to w. Ephemeral lanes
// only ever live on the host that runs them, so this is inherently a host-local
// read; run it on the host (root) so journalctl can read the slot unit's journal.
func (m *Manager) EphemeralLogs(ctx context.Context, org, slot string, opts LogOptions, w io.Writer) error {
	org, err := m.requireOrg(org)
	if err != nil {
		return err
	}
	return m.orchestratorFor(org).EphemeralLog(ctx, org, slot, opts, w)
}
