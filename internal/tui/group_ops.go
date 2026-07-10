package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

// Runner-group management commands (the Groups tab CRUD + repo-access picker).

// groupSavedMsg reports the outcome of a create/update. When openPicker is set the
// group is "selected"-visibility, so the Model chains into the repo picker for the
// returned org/id.
type groupSavedMsg struct {
	org        string
	id         int64
	name       string
	openPicker bool
	summary    string
	err        error
}

// reposLoadedMsg carries the repo picker's data: the org's repos (candidates) and
// the group's currently-assigned repos (pre-selection).
type reposLoadedMsg struct {
	all     []core.Repo
	current []core.Repo
	err     error
}

// jobLookupMsg carries the async current-job lookup result for the Info panel.
type jobLookupMsg struct {
	job   *core.RunnerJob
	found bool
	err   error
}

// jobLookupCmd scans the org's accessible repos for the job a busy runner is on.
func jobLookupCmd(ctx context.Context, mgr *service.Manager, org, runnerName string) tea.Cmd {
	return func() tea.Msg {
		job, found, err := mgr.RunnerCurrentJob(ctx, org, runnerName)
		return jobLookupMsg{job: job, found: found, err: err}
	}
}

// groupReposInfoMsg carries a group's assigned repos for the Info panel.
type groupReposInfoMsg struct {
	repos []core.Repo
	err   error
}

// groupReposInfoCmd loads a group's assigned repos (runner-group API; returns them
// regardless of the App's own repo access).
func groupReposInfoCmd(ctx context.Context, mgr *service.Manager, org string, groupID int64) tea.Cmd {
	return func() tea.Msg {
		repos, err := mgr.ListGroupRepos(ctx, org, groupID)
		return groupReposInfoMsg{repos: repos, err: err}
	}
}

func createGroupCmd(ctx context.Context, mgr *service.Manager, org, name, visibility string) tea.Cmd {
	return func() tea.Msg {
		g, err := mgr.CreateGroup(ctx, org, name, visibility)
		if err != nil {
			return groupSavedMsg{err: err}
		}
		return groupSavedMsg{
			org: org, id: g.ID, name: name,
			openPicker: visibility == "selected",
			summary:    fmt.Sprintf("created group %q (%s)", name, org),
		}
	}
}

func updateGroupCmd(ctx context.Context, mgr *service.Manager, org string, id int64, name, visibility string) tea.Cmd {
	return func() tea.Msg {
		if _, err := mgr.UpdateGroup(ctx, org, id, name, visibility); err != nil {
			return groupSavedMsg{err: err}
		}
		return groupSavedMsg{
			org: org, id: id, name: name,
			openPicker: visibility == "selected",
			summary:    fmt.Sprintf("updated group %q (%s)", name, org),
		}
	}
}

// runnerGroupsMsg carries the org's group names for the runner group-edit Select
// (loaded async when e is pressed on a persistent runner).
type runnerGroupsMsg struct {
	runner service.FusedRunner
	groups []string
	err    error
}

// loadRunnerGroupsCmd lists the runner's org groups to populate the edit Select.
func loadRunnerGroupsCmd(ctx context.Context, mgr *service.Manager, r service.FusedRunner) tea.Cmd {
	return func() tea.Msg {
		gs, err := mgr.ListGroups(ctx, r.Org)
		if err != nil {
			return runnerGroupsMsg{runner: r, err: err}
		}
		names := make([]string, 0, len(gs))
		for _, g := range gs {
			names = append(names, g.Name)
		}
		return runnerGroupsMsg{runner: r, groups: names}
	}
}

// moveRunnerCmd moves a persistent runner into the named (existing) group.
func moveRunnerCmd(ctx context.Context, mgr *service.Manager, org string, runnerID int64, group, name string) tea.Cmd {
	return func() tea.Msg {
		if err := mgr.MoveRunnerToGroupByName(ctx, org, runnerID, group); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{summary: fmt.Sprintf("moved %s to group %q", name, group)}
	}
}

func deleteGroupCmd(ctx context.Context, mgr *service.Manager, org string, id int64, name string) tea.Cmd {
	return func() tea.Msg {
		if err := mgr.DeleteGroup(ctx, org, id); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{summary: fmt.Sprintf("deleted group %q (%s)", name, org)}
	}
}

// loadReposCmd fetches the picker's candidate repos + the group's current repos.
func loadReposCmd(ctx context.Context, mgr *service.Manager, org string, groupID int64) tea.Cmd {
	return func() tea.Msg {
		all, err := mgr.ListOrgRepos(ctx, org)
		if err != nil {
			return reposLoadedMsg{err: err}
		}
		current, cerr := mgr.ListGroupRepos(ctx, org, groupID)
		if cerr != nil {
			// Candidates loaded; pre-selection unavailable - start from an empty set
			// rather than failing the whole picker.
			return reposLoadedMsg{all: all}
		}
		return reposLoadedMsg{all: all, current: current}
	}
}

// setGroupReposCmd writes the chosen repo set to the group.
func setGroupReposCmd(ctx context.Context, mgr *service.Manager, org string, groupID int64, ids []int64) tea.Cmd {
	return func() tea.Msg {
		if err := mgr.SetGroupRepos(ctx, org, groupID, ids); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{summary: fmt.Sprintf("set %d repo(s) on the group", len(ids))}
	}
}
