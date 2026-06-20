package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

// loadRunnersCmd fetches PERSISTENT runners off the event loop. orgFilter == ""
// means all configured orgs (with host-locality); otherwise a single org. Ephemeral
// JIT registrations are excluded - they churn every job and belong to the Ephemeral
// panel, so the two natures are never shown together.
func loadRunnersCmd(ctx context.Context, mgr *service.Manager, orgFilter string) tea.Cmd {
	return func() tea.Msg {
		if orgFilter != "" {
			rs, err := mgr.ListRunners(ctx, orgFilter)
			if err != nil {
				return runnersMsg{err: err}
			}
			rows := make([]service.RunnerWithOrg, 0, len(rs))
			for _, r := range rs {
				if service.IsEphemeralRunnerName(r.Name) {
					continue
				}
				rows = append(rows, service.RunnerWithOrg{
					Org:    orgFilter,
					Runner: r,
					Local:  mgr.RunnerIsLocal(orgFilter, r.Name),
				})
			}
			return runnersMsg{rows: rows}
		}
		all, errs := mgr.ListAllRunners(ctx)
		rows := make([]service.RunnerWithOrg, 0, len(all))
		for _, row := range all {
			if service.IsEphemeralRunnerName(row.Runner.Name) {
				continue
			}
			rows = append(rows, row)
		}
		return runnersMsg{rows: rows, err: firstErr(errs)}
	}
}

// loadEphemeralCmd fetches the host-local ephemeral slot lanes off the event loop.
func loadEphemeralCmd(ctx context.Context, mgr *service.Manager, orgFilter string) tea.Cmd {
	return func() tea.Msg {
		rows, err := mgr.ListEphemeralSlots(ctx, orgFilter)
		return ephemeralMsg{rows: rows, err: err}
	}
}

// loadSettingsCmd resolves the current capacity policy into a display snapshot.
// It reads only in-memory config, so it never blocks, but stays a tea.Cmd to fit
// the load/refresh pattern (and to refresh after a save).
func loadSettingsCmd(mgr *service.Manager) tea.Cmd {
	return func() tea.Msg {
		cfg := mgr.Config()
		perOrg := false
		for _, oc := range cfg.Orgs {
			if !oc.Resources.IsZero() {
				perOrg = true
				break
			}
		}
		var sliceMax string
		if cfg.ResourceMode == config.ResourceModeAuto {
			sliceMax = cfg.SliceMemoryMaxOrDefault()
		}
		return settingsMsg{snap: settingsSnapshot{
			Mode:      cfg.ResourceMode,
			Effective: cfg.ResourcesFor(""),
			Overrides: cfg.Resources,
			SliceMax:  sliceMax,
			SliceSet:  cfg.SliceMemoryMax != "",
			PerOrg:    perOrg,
			Path:      mgr.ConfigPath(),
		}}
	}
}

// loadGroupsCmd fetches runner groups (all orgs, or one).
func loadGroupsCmd(ctx context.Context, mgr *service.Manager, orgFilter string) tea.Cmd {
	return func() tea.Msg {
		if orgFilter != "" {
			gs, err := mgr.ListGroups(ctx, orgFilter)
			if err != nil {
				return groupsMsg{err: err}
			}
			rows := make([]service.GroupWithOrg, 0, len(gs))
			for _, g := range gs {
				rows = append(rows, service.GroupWithOrg{Org: orgFilter, Group: g})
			}
			return groupsMsg{rows: rows}
		}
		rows, errs := mgr.ListAllGroups(ctx)
		return groupsMsg{rows: rows, err: firstErr(errs)}
	}
}

// loadHealthCmd checks auth + retention per org (all, or one).
func loadHealthCmd(ctx context.Context, mgr *service.Manager, orgFilter string) tea.Cmd {
	return func() tea.Msg {
		orgs := mgr.OrgNames()
		if orgFilter != "" {
			orgs = []string{orgFilter}
		}
		reps := make([]healthReport, 0, len(orgs))
		for _, org := range orgs {
			rep := healthReport{Org: org}
			rs, err := mgr.ListRunners(ctx, org)
			if err != nil {
				rep.AuthErr = err
			} else {
				rep.Runners = len(rs)
				for _, r := range rs {
					if r.Online() {
						rep.Online++
					}
				}
			}
			if ret, err := mgr.Retention(ctx, org); err != nil {
				rep.RetentionErr = err
			} else {
				rep.RetentionDays = ret.Days
				rep.RetentionMax = ret.MaxAllowedDays
			}
			reps = append(reps, rep)
		}
		return healthMsg{reports: reps}
	}
}

// deleteRunnerCmd deregisters a single (remote) runner via the API.
func deleteRunnerCmd(ctx context.Context, mgr *service.Manager, org string, id int64, name string) tea.Cmd {
	return func() tea.Msg {
		if err := mgr.DeleteRunner(ctx, org, id); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{summary: fmt.Sprintf("deregistered %s (%s)", name, org)}
	}
}

// destroyRunnerCmd tears down a runner that lives on THIS host (stops + removes
// the systemd unit and tree) and deregisters it.
func destroyRunnerCmd(ctx context.Context, mgr *service.Manager, org, name string) tea.Cmd {
	return func() tea.Msg {
		if err := mgr.DestroyRunner(ctx, org, name); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{summary: fmt.Sprintf("destroyed %s (%s)", name, org)}
	}
}

// destroyEphemeralSlotCmd removes an ephemeral slot lane from THIS host (stops +
// deletes its unit and tree) and deregisters any in-flight JIT registration.
func destroyEphemeralSlotCmd(ctx context.Context, mgr *service.Manager, org, slot string) tea.Cmd {
	return func() tea.Msg {
		if err := mgr.DestroyEphemeralSlot(ctx, org, slot); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{summary: fmt.Sprintf("destroyed ephemeral slot %s (%s)", slot, org)}
	}
}

// saveSettingsCmd persists the edited capacity policy, then reloads the snapshot.
func saveSettingsCmd(mgr *service.Manager, s service.ResourceSettings) tea.Cmd {
	return func() tea.Msg {
		if err := mgr.ApplyResourceSettings(s); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{summary: "saved capacity settings → " + mgr.ConfigPath()}
	}
}

// progressMsg carries one runner-creation event to the progress bar.
type progressMsg struct{ ev core.ProgressEvent }

// createDoneMsg is the final outcome once the create goroutine finishes.
type createDoneMsg struct {
	summary string
	err     error
}

type createResult struct {
	names []string
	err   error
}

// startCreate launches CreateRunners in a goroutine, streaming per-runner events
// over the returned event channel and the final result over the result channel.
// The model holds both so waitCreateCmd can drain them across Update cycles.
// Must run as root on the target host; from a remote box it surfaces the error.
func startCreate(ctx context.Context, mgr *service.Manager, spec service.DeploySpec) (chan core.ProgressEvent, chan createResult) {
	ch := make(chan core.ProgressEvent)
	resCh := make(chan createResult, 1)
	go func() {
		names, err := mgr.CreateRunners(ctx, spec, ch)
		close(ch)
		resCh <- createResult{names: names, err: err}
	}()
	return ch, resCh
}

// startCreateEphemeral is the ephemeral-slot analogue of startCreate: it stands up
// the requested slot lanes, streaming the same per-item progress so the wizard's
// progress bar is reused verbatim.
func startCreateEphemeral(ctx context.Context, mgr *service.Manager, spec service.DeploySpec) (chan core.ProgressEvent, chan createResult) {
	ch := make(chan core.ProgressEvent)
	resCh := make(chan createResult, 1)
	go func() {
		slots, err := mgr.CreateEphemeralRunners(ctx, spec, ch)
		close(ch)
		resCh <- createResult{names: slots, err: err}
	}()
	return ch, resCh
}

// waitCreateCmd blocks for the next event; when the stream closes it reads the
// final result and emits createDoneMsg. noun labels the created items in the
// summary ("runner" or "ephemeral slot").
func waitCreateCmd(ch chan core.ProgressEvent, resCh chan createResult, org, noun string) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			res := <-resCh
			if res.err != nil {
				if len(res.names) > 0 {
					return createDoneMsg{err: fmt.Errorf("created %d, then failed: %w", len(res.names), res.err)}
				}
				return createDoneMsg{err: res.err}
			}
			return createDoneMsg{summary: fmt.Sprintf("created %d %s(s) in %s", len(res.names), noun, org)}
		}
		return progressMsg{ev: ev}
	}
}

// firstErr returns any one error from a per-org error map (for a "partial
// failure" warning).
func firstErr(errs map[string]error) error {
	for org, e := range errs {
		if e != nil {
			return fmt.Errorf("%s: %w", org, e)
		}
	}
	return nil
}
