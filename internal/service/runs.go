package service

import (
	"context"
	"sort"

	"golang.org/x/sync/errgroup"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/runner"
)

// ActiveRun is a runner currently executing a workflow job, joined LIVE from GitHub.
// It is the "active now" half of the Runs view (the durable joblog store is the
// history half): the runner (its slot, when ephemeral) related to the repo, workflow,
// and job it is running right now. Status is implicitly in-progress - that is what a
// busy runner + a matched in-progress job means.
type ActiveRun struct {
	Org        string
	RunnerName string
	Slot       string // ephemeral slot id ("" for a persistent runner)
	Ephemeral  bool
	Job        core.RunnerJob
}

// ActiveRuns returns the jobs the fleet's BUSY runners are executing right now,
// resolved live from GitHub. There is no runner->job endpoint, so each busy runner's
// job is found by scanning its org's in-progress runs (bounded + short-circuiting,
// see RunnerCurrentJob); the per-runner lookups run concurrently, bounded by the
// configured concurrency. orgFilter ("" = all) scopes it. Best-effort: a busy runner
// whose job can't be resolved (App lacks Actions:Read, or it just finished) is simply
// omitted rather than failing the whole view. Ordered by org, then runner name.
func (m *Manager) ActiveRuns(ctx context.Context, orgFilter string) ([]ActiveRun, error) {
	all, _ := m.ListAllRunners(ctx) // a partial org-list failure must not sink the active view
	var busy []RunnerWithOrg
	for _, r := range all {
		if orgFilter != "" && r.Org != orgFilter {
			continue
		}
		if r.Runner.Busy {
			busy = append(busy, r)
		}
	}
	if len(busy) == 0 {
		return nil, nil
	}

	out := make([]ActiveRun, len(busy))
	hit := make([]bool, len(busy))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(scanConcurrency(m.cfg.Concurrency))
	for i, r := range busy {
		g.Go(func() error {
			job, ok, err := m.RunnerCurrentJob(gctx, r.Org, r.Runner.Name)
			if err != nil || !ok || job == nil {
				return nil // best-effort: skip a runner whose job can't be resolved
			}
			ar := ActiveRun{Org: r.Org, RunnerName: r.Runner.Name, Job: *job}
			if IsEphemeralRunnerName(r.Runner.Name) {
				ar.Ephemeral = true
				if s, ok := runner.EphemeralSlotFromName(r.Runner.Name, r.Org); ok {
					ar.Slot = s
				}
			}
			out[i], hit[i] = ar, true
			return nil
		})
	}
	_ = g.Wait() // per-runner errors are swallowed above; Wait never returns one

	runs := make([]ActiveRun, 0, len(busy))
	for i := range out {
		if hit[i] {
			runs = append(runs, out[i])
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].Org != runs[j].Org {
			return runs[i].Org < runs[j].Org
		}
		return runs[i].RunnerName < runs[j].RunnerName
	})
	return runs, nil
}
