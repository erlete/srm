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
