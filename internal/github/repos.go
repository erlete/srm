package github

import (
	"context"
	"sort"

	gh "github.com/google/go-github/v88/github"

	"github.com/erlete/srm/internal/core"
)

// ListOrgRepos returns the repositories this org's GitHub App can enumerate, for
// the runner-group repo-access picker. It UNIONS two sources and dedupes by id:
//   - GET /orgs/{org}/repos (RepositoriesService.ListByOrg) - the org's repos
//     visible to the token (public, plus any private the App is granted).
//   - GET /installation/repositories (AppsService.ListRepos) - the repos the App
//     installation is explicitly granted (often the only place private repos show).
//
// Different installations surface repos through different endpoints, so the union
// maximizes coverage. It returns an error only when BOTH calls fail (so a single
// flaky endpoint never empties the picker). Private repos appear only if the App
// has repository access; an org-perms-only App will see public repos only.
func (c *client) ListOrgRepos(ctx context.Context, org string) ([]core.Repo, error) {
	seen := map[int64]bool{}
	var out []core.Repo
	add := func(r *gh.Repository) {
		if r == nil || seen[r.GetID()] {
			return
		}
		seen[r.GetID()] = true
		out = append(out, toCoreRepo(r))
	}

	orgErr := c.eachOrgRepo(ctx, org, add)
	instErr := c.eachInstallationRepo(ctx, add)
	if len(out) == 0 {
		if orgErr != nil {
			return nil, orgErr
		}
		if instErr != nil {
			return nil, instErr
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FullName < out[j].FullName })
	return out, nil
}

// eachOrgRepo pages GET /orgs/{org}/repos.
func (c *client) eachOrgRepo(ctx context.Context, org string, fn func(*gh.Repository)) error {
	opts := &gh.RepositoryListByOrgOptions{Type: "all", ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		repos, resp, err := c.gh.Repositories.ListByOrg(ctx, org, opts)
		if err != nil {
			return err
		}
		for _, r := range repos {
			fn(r)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return nil
}

// FindRunnerJob scans a repo's in-progress workflow runs for a job currently
// assigned to runnerName, returning it if found. GitHub has no runner->job index,
// so the caller iterates the org's accessible repos and stops at the first hit.
// Requires Actions: Read on the repo; a 403/404 surfaces as err (the caller treats
// it as "not visible"). Only the first page of in-progress runs is scanned (a
// runner runs one job at a time, so recent runs are what matter).
func (c *client) FindRunnerJob(ctx context.Context, owner, repo, runnerName string) (*core.RunnerJob, bool, error) {
	runs, _, err := c.gh.Actions.ListRepositoryWorkflowRuns(ctx, owner, repo, &gh.ListWorkflowRunsOptions{
		Status:      "in_progress",
		ListOptions: gh.ListOptions{PerPage: 50},
	})
	if err != nil {
		return nil, false, err
	}
	for _, run := range runs.WorkflowRuns {
		jobs, _, jerr := c.gh.Actions.ListWorkflowJobs(ctx, owner, repo, run.GetID(), &gh.ListWorkflowJobsOptions{
			Filter:      "latest",
			ListOptions: gh.ListOptions{PerPage: 100},
		})
		if jerr != nil {
			return nil, false, jerr
		}
		for _, j := range jobs.Jobs {
			if j.GetRunnerName() == runnerName && j.GetStatus() == "in_progress" {
				return &core.RunnerJob{
					Repo:     owner + "/" + repo,
					Workflow: j.GetWorkflowName(),
					JobName:  j.GetName(),
					URL:      j.GetHTMLURL(),
				}, true, nil
			}
		}
	}
	return nil, false, nil
}

// eachInstallationRepo pages GET /installation/repositories.
func (c *client) eachInstallationRepo(ctx context.Context, fn func(*gh.Repository)) error {
	opts := &gh.ListOptions{PerPage: 100}
	for {
		repos, resp, err := c.gh.Apps.ListRepos(ctx, opts)
		if err != nil {
			return err
		}
		for _, r := range repos.Repositories {
			fn(r)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return nil
}
