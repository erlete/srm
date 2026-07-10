package github

import (
	"context"

	gh "github.com/google/go-github/v88/github"

	"github.com/erlete/srm/internal/core"
)

// CreateGroup creates an org runner group with the given visibility
// (all|selected|private).
func (c *client) CreateGroup(ctx context.Context, org, name, visibility string) (core.Group, error) {
	g, _, err := c.gh.Actions.CreateOrganizationRunnerGroup(ctx, org, gh.CreateRunnerGroupRequest{
		Name:       gh.Ptr(name),
		Visibility: gh.Ptr(visibility),
	})
	if err != nil {
		return core.Group{}, err
	}
	return toCoreGroup(g), nil
}

// UpdateGroup renames a runner group and/or changes its visibility
// (all|selected|private). Empty fields are left unchanged.
func (c *client) UpdateGroup(ctx context.Context, org string, id int64, name, visibility string) (core.Group, error) {
	req := gh.UpdateRunnerGroupRequest{}
	if name != "" {
		req.Name = gh.Ptr(name)
	}
	if visibility != "" {
		req.Visibility = gh.Ptr(visibility)
	}
	g, _, err := c.gh.Actions.UpdateOrganizationRunnerGroup(ctx, org, id, req)
	if err != nil {
		return core.Group{}, err
	}
	return toCoreGroup(g), nil
}

// DeleteGroup removes a runner group. GitHub forbids deleting the Default group
// (id 1); the service layer refuses that before calling here.
func (c *client) DeleteGroup(ctx context.Context, org string, id int64) error {
	_, err := c.gh.Actions.DeleteOrganizationRunnerGroup(ctx, org, id)
	return err
}

// ListGroupRepos returns the repositories currently granted access to a runner
// group (meaningful for visibility "selected"), paginated.
func (c *client) ListGroupRepos(ctx context.Context, org string, groupID int64) ([]core.Repo, error) {
	opts := &gh.ListOptions{PerPage: 100}
	var out []core.Repo
	for {
		repos, resp, err := c.gh.Actions.ListRepositoryAccessRunnerGroup(ctx, org, groupID, opts)
		if err != nil {
			return nil, err
		}
		for _, r := range repos.Repositories {
			out = append(out, toCoreRepo(r))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// SetGroupRepos replaces the set of repositories that can access a runner group
// (the GitHub "Repository access" list). Passing the full desired id set is the
// single-call replace the web UI uses.
func (c *client) SetGroupRepos(ctx context.Context, org string, groupID int64, repoIDs []int64) error {
	_, err := c.gh.Actions.SetRepositoryAccessRunnerGroup(ctx, org, groupID, gh.SetRepoAccessRunnerGroupRequest{
		SelectedRepositoryIDs: repoIDs,
	})
	return err
}

// ListGroupRunnerIDs returns the runner ids currently in a group, paginated. The
// org-runners list API does not report each runner's group, so the membership map
// is built by listing every group's runners (each runner is in exactly one group).
func (c *client) ListGroupRunnerIDs(ctx context.Context, org string, groupID int64) ([]int64, error) {
	opts := &gh.ListOptions{PerPage: 100}
	var out []int64
	for {
		runners, resp, err := c.gh.Actions.ListRunnerGroupRunners(ctx, org, groupID, opts)
		if err != nil {
			return nil, err
		}
		for _, r := range runners.Runners {
			out = append(out, r.GetID())
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// AddRunnerToGroup moves a runner into a group (GitHub has no atomic "move"; the
// runner leaves its current group implicitly when added to the new one).
func (c *client) AddRunnerToGroup(ctx context.Context, org string, groupID, runnerID int64) error {
	_, err := c.gh.Actions.AddRunnerGroupRunners(ctx, org, groupID, runnerID)
	return err
}

// RemoveRunnerFromGroup removes a runner from a group (it falls back to Default).
func (c *client) RemoveRunnerFromGroup(ctx context.Context, org string, groupID, runnerID int64) error {
	_, err := c.gh.Actions.RemoveRunnerGroupRunners(ctx, org, groupID, runnerID)
	return err
}

// ListGroups returns the org's runner groups (including any inherited from the
// enterprise), paginated.
func (c *client) ListGroups(ctx context.Context, org string) ([]core.Group, error) {
	opts := &gh.ListOrgRunnerGroupOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	var out []core.Group
	for {
		groups, resp, err := c.gh.Actions.ListOrganizationRunnerGroups(ctx, org, opts)
		if err != nil {
			return nil, err
		}
		for _, g := range groups.RunnerGroups {
			out = append(out, toCoreGroup(g))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}
