package tui

import (
	"context"
	"fmt"
	"os/exec"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

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
			Policy:    mgr.HostPolicy(),
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

// loadHealthCmd is the TUI doctor: auth + retention + agent-freshness per org,
// plus a host-wide block (capacity mode + toolchain probes).
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
			cur, total, behind, ok := mgr.AgentVersionStatus(ctx, org)
			rep.AgentCurrent, rep.AgentBehind, rep.AgentTotal, rep.AgentOK = cur, len(behind), total, ok
			if acc, aerr := mgr.AppInstallationAccess(ctx, org); aerr == nil {
				rep.AppAccessOK = true
				rep.AppRepoSelection = acc.RepositorySelection
				rep.AppPrivateInvisible = !acc.SeesPrivateRepos
			}
			reps = append(reps, rep)
		}
		return healthMsg{reports: reps, host: hostDoctor(ctx, mgr, orgs)}
	}
}

// hostDoctor probes this host: capacity mode, the box-wide stats (slice/disk/cache
// via HostHealth), whether the build toolchain the runners rely on is present, the
// configured dependency-manifest drift, and (when docker.rootlessDinD is on) the
// rootless-Docker readiness. exec.LookPath works on both OSes; the stats degrade
// off-host. orgs scopes the manifest/DinD probes to the same set as the org cards.
func hostDoctor(ctx context.Context, mgr *service.Manager, orgs []string) healthHost {
	mode := mgr.Config().ResourceMode
	switch mode {
	case "":
		mode = "off (no per-runner caps)"
	case config.ResourceModeAuto:
		mode = "auto (machine-relative, scales with host RAM)"
	default:
		mode = mode + " (manual literal caps)"
	}
	tools := []string{"git", "docker", "node", "npm", "pnpm", "make"}
	probes := make([]toolProbe, 0, len(tools))
	for _, name := range tools {
		path, err := exec.LookPath(name)
		probes = append(probes, toolProbe{Name: name, Path: path, Found: err == nil})
	}
	host := healthHost{CapacityMode: mode, Tools: probes, Stats: mgr.HostHealth(ctx)}

	// Dependency-manifest drift (M3): only meaningful when a host manifest is
	// configured. A probe error leaves ManifestOK false (the panel renders "unknown").
	if man := mgr.HostManifest(); !man.Empty() {
		host.ManifestSet = true
		if missing, err := mgr.HostDrift(ctx, man); err == nil {
			host.ManifestMissing, host.ManifestOK = missing, true
		}
	}
	// Rootless-DinD host readiness (M3): Enabled=false unless docker.rootlessDinD is on.
	host.DinD = mgr.DinDReadiness(orgs)
	return host
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

// saveRetentionCmd updates the org's artifact-and-log retention period.
func saveRetentionCmd(ctx context.Context, mgr *service.Manager, org string, days int) tea.Cmd {
	return func() tea.Msg {
		if err := mgr.SetRetention(ctx, org, days); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{summary: fmt.Sprintf("set %s retention to %d days", org, days)}
	}
}

// saveSettingsCmd persists the edited capacity policy and host-wide policy toggles,
// then reloads the snapshot.
func saveSettingsCmd(mgr *service.Manager, s service.ResourceSettings, pol service.HostPolicy) tea.Cmd {
	return func() tea.Msg {
		if err := mgr.ApplyResourceSettings(s); err != nil {
			return actionMsg{err: err}
		}
		if err := mgr.ApplyHostPolicy(pol); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{summary: "saved settings → " + mgr.ConfigPath()}
	}
}

// saveManifestCmd persists the edited host dependency manifest. It does NOT apply it
// to the host - that is the Provision op (`srm provision`), which the summary points at.
func saveManifestCmd(mgr *service.Manager, man core.DependencyManifest) tea.Cmd {
	return func() tea.Msg {
		if err := mgr.ApplyHostManifest(man); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{summary: "saved host manifest → " + mgr.ConfigPath() + " (run Provision to apply)"}
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
