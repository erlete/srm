package service

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/erlete/srm/internal/joblog"
	"github.com/erlete/srm/internal/runner"
)

// JobLogStore returns the durable ephemeral job-log store (root-owned; see jobLogsRoot).
// The CLI/TUI readers and the prune sweep all go through it.
func (m *Manager) JobLogStore() joblog.Store { return joblog.Store{Root: jobLogsRoot} }

// captureJobLog copies a just-finished ephemeral job's _diag log into the durable
// store (with a sidecar) BEFORE the next cycle wipes _diag. It is BEST-EFFORT: a
// capture failure is logged but never fails the job cycle (a lost log must not break
// runner service). Called from RunCycle, which runs as root.
func (m *Manager) captureJobLog(org, slot, runnerName string, runnerID int64, started, ended time.Time, runErr error) {
	diag := m.orchestratorFor(org).EphemeralDiagDir(org, slot)
	src := runner.NewestJobLog(diag) // "" if the job wrote no log; the sidecar is still recorded
	meta := joblog.Meta{
		Org:          org,
		Slot:         slot,
		RunnerName:   runnerName,
		RunnerID:     runnerID,
		StartedUnix:  started.Unix(),
		EndedUnix:    ended.Unix(),
		OK:           runErr == nil,
		CapturedUnix: time.Now().Unix(),
	}
	if runErr != nil {
		meta.Error = runErr.Error()
	}
	if err := m.JobLogStore().Write(meta, src); err != nil {
		fmt.Fprintf(os.Stderr, "srm: job-log capture failed for %s/%s (runner %d): %v\n", org, slot, runnerID, err)
	}
}

// PruneJobLogs deletes captured job logs older than maxAge (honoring DryRun). It is
// folded into `srm cache prune` so one sweep bounds both the dep cache and the logs.
func (m *Manager) PruneJobLogs(_ context.Context, maxAge time.Duration) (entries int, bytes int64, err error) {
	return m.JobLogStore().Prune(time.Now().Add(-maxAge), m.cfg.DryRun)
}
