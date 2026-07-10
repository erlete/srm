package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/erlete/srm/internal/core"
)

// RunnerCurrentJob finds the workflow job a runner is executing by scanning the
// org's accessible repos' in-progress runs (there is no runner->job endpoint). It
// covers every repo the App can read, scanned concurrently (bounded by the config
// concurrency) and ordered most-recently-pushed first, short-circuiting on the
// first match. It is best-effort and permission-bounded: repos the App lacks
// Actions:Read on are skipped. Returns (job, true) on a match, (nil, false) when
// no scanned repo has a job for this runner.
func (m *Manager) RunnerCurrentJob(ctx context.Context, org, runnerName string) (*core.RunnerJob, bool, error) {
	org, err := m.requireOrg(org)
	if err != nil {
		return nil, false, err
	}
	repos, err := m.ListOrgRepos(ctx, org)
	if err != nil {
		return nil, false, err
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return nil, false, err
	}
	// A busy runner's job almost always lives in a repo with recent activity, so
	// probing most-recently-pushed repos first makes the short-circuit return
	// after a handful of calls in the common case (empty PushedAt sorts last).
	sort.SliceStable(repos, func(i, j int) bool { return repos[i].PushedAt > repos[j].PushedAt })

	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu    sync.Mutex
		found *core.RunnerJob
	)
	g := new(errgroup.Group)
	g.SetLimit(scanConcurrency(m.cfg.Concurrency))
	for _, r := range repos {
		g.Go(func() error {
			if scanCtx.Err() != nil {
				return nil // a sibling already matched (or the caller canceled)
			}
			owner, name, ok := strings.Cut(r.FullName, "/")
			if !ok {
				return nil
			}
			job, ok, ferr := c.FindRunnerJob(scanCtx, owner, name, runnerName)
			if ferr != nil || !ok {
				return nil // no Actions:Read, canceled, or no match - skip
			}
			mu.Lock()
			if found == nil {
				found = job
			}
			mu.Unlock()
			cancel() // stop the remaining probes
			return nil
		})
	}
	_ = g.Wait()
	if found != nil {
		return found, true, nil
	}
	return nil, false, nil
}

// scanConcurrency clamps the configured concurrency for the current-job scan,
// defaulting to 8 when unset so the scan always parallelizes.
func scanConcurrency(n int) int {
	if n <= 0 {
		return 8
	}
	return n
}

// Runner-group management: the service wrappers behind the TUI Groups tab (create,
// edit, delete, and repository-access assignment). All honor dry-run and validate
// the org. CreateGroup / GetOrCreateGroup live in deploy.go (the create path).

// DefaultGroupID is the org "Default" runner group, which GitHub forbids deleting
// or renaming.
const DefaultGroupID = 1

// UpdateGroup renames a group and/or changes its visibility (all|selected|private).
// Refuses the Default group (GitHub forbids editing it). Honors dry-run.
func (m *Manager) UpdateGroup(ctx context.Context, org string, id int64, name, visibility string) (core.Group, error) {
	org, err := m.requireOrg(org)
	if err != nil {
		return core.Group{}, err
	}
	if id == DefaultGroupID {
		return core.Group{}, fmt.Errorf("the Default group cannot be edited")
	}
	if m.cfg.DryRun {
		return core.Group{ID: id, Name: name, Visibility: visibility}, nil
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return core.Group{}, err
	}
	return c.UpdateGroup(ctx, org, id, name, visibility)
}

// DeleteGroup removes a runner group. Refuses the Default group (id 1), which
// GitHub forbids deleting. Runners in a deleted group fall back to Default. Honors
// dry-run.
func (m *Manager) DeleteGroup(ctx context.Context, org string, id int64) error {
	org, err := m.requireOrg(org)
	if err != nil {
		return err
	}
	if id == DefaultGroupID {
		return fmt.Errorf("the Default group cannot be deleted")
	}
	if m.cfg.DryRun {
		return nil
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return err
	}
	return c.DeleteGroup(ctx, org, id)
}

// ListGroupRepos returns the repositories currently granted access to a group
// (used to pre-populate the repo picker for a "selected"-visibility group).
func (m *Manager) ListGroupRepos(ctx context.Context, org string, groupID int64) ([]core.Repo, error) {
	org, err := m.requireOrg(org)
	if err != nil {
		return nil, err
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return nil, err
	}
	return c.ListGroupRepos(ctx, org, groupID)
}

// SetGroupRepos replaces the repository-access set for a group (the GitHub web
// "Repository access" list). Honors dry-run.
func (m *Manager) SetGroupRepos(ctx context.Context, org string, groupID int64, repoIDs []int64) error {
	org, err := m.requireOrg(org)
	if err != nil {
		return err
	}
	if m.cfg.DryRun {
		return nil
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return err
	}
	return c.SetGroupRepos(ctx, org, groupID, repoIDs)
}

// ListOrgRepos returns the org's repositories for the repo-access picker's
// autocomplete candidate list.
func (m *Manager) ListOrgRepos(ctx context.Context, org string) ([]core.Repo, error) {
	org, err := m.requireOrg(org)
	if err != nil {
		return nil, err
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return nil, err
	}
	return c.ListOrgRepos(ctx, org)
}

// RunnerGroups returns, for an org, the runnerID -> groupID membership map and the
// groupID -> name map, built by listing every group's runners (the org-runners API
// omits each runner's group). Best-effort and observational: a failure yields
// partial/empty maps so the GROUP column simply blanks rather than failing the load.
func (m *Manager) RunnerGroups(ctx context.Context, org string) (toGroup map[int64]int64, names map[int64]string) {
	toGroup, names = map[int64]int64{}, map[int64]string{}
	c, err := m.client(ctx, org)
	if err != nil {
		return toGroup, names
	}
	groups, err := c.ListGroups(ctx, org)
	if err != nil {
		return toGroup, names
	}
	for _, g := range groups {
		names[g.ID] = g.Name
		ids, err := c.ListGroupRunnerIDs(ctx, org, g.ID)
		if err != nil {
			continue
		}
		for _, id := range ids {
			toGroup[id] = g.ID
		}
	}
	return toGroup, names
}

// MoveRunnerToGroupByName moves a runner into the named group, resolving the name
// to its id first. Errors if no such group exists (the group must already exist;
// use the Groups tab to create one). Honors dry-run.
func (m *Manager) MoveRunnerToGroupByName(ctx context.Context, org string, runnerID int64, groupName string) error {
	org, err := m.requireOrg(org)
	if err != nil {
		return err
	}
	groups, err := m.ListGroups(ctx, org)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.Name == groupName {
			return m.MoveRunnerToGroup(ctx, org, runnerID, g.ID)
		}
	}
	return fmt.Errorf("no runner group named %q in %s (create it on the Groups tab first)", groupName, org)
}

// MoveRunnerToGroup moves a runner into a group. GitHub has no atomic move: adding
// a runner to a group removes it from its previous one, so a single add suffices.
// Honors dry-run.
func (m *Manager) MoveRunnerToGroup(ctx context.Context, org string, runnerID, groupID int64) error {
	org, err := m.requireOrg(org)
	if err != nil {
		return err
	}
	if m.cfg.DryRun {
		return nil
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return err
	}
	return c.AddRunnerToGroup(ctx, org, groupID, runnerID)
}
