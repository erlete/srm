package github

import (
	"context"
	"errors"
	"net/http"

	gh "github.com/google/go-github/v88/github"

	"github.com/erlete/srm/internal/core"
)

// IsNotFound reports whether err is a GitHub 404 — e.g. a runner that has already
// been deleted (or auto-deregistered after a JIT job). Lets callers treat a
// double-delete as success and distinguish "gone" from a transient API error.
func IsNotFound(err error) bool {
	var er *gh.ErrorResponse
	if errors.As(err, &er) {
		return er.Response != nil && er.Response.StatusCode == http.StatusNotFound
	}
	return false
}

// ListRunners returns every org runner, following the Link-header pagination
// with per_page=100.
func (c *client) ListRunners(ctx context.Context, org string) ([]core.Runner, error) {
	opts := &gh.ListRunnersOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	var out []core.Runner
	for {
		runners, resp, err := c.gh.Actions.ListOrganizationRunners(ctx, org, opts)
		if err != nil {
			return nil, err
		}
		for _, r := range runners.Runners {
			out = append(out, toCoreRunner(r))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// GetRunner fetches a single runner by id.
func (c *client) GetRunner(ctx context.Context, org string, id int64) (core.Runner, error) {
	r, _, err := c.gh.Actions.GetOrganizationRunner(ctx, org, id)
	if err != nil {
		return core.Runner{}, err
	}
	return toCoreRunner(r), nil
}

// DeleteRunner force-removes a runner from GitHub (there is no bulk endpoint).
func (c *client) DeleteRunner(ctx context.Context, org string, id int64) error {
	_, err := c.gh.Actions.RemoveOrganizationRunner(ctx, org, id)
	return err
}

// GenerateJITConfig mints a single-use JIT runner config. The returned
// EncodedJITConfig is passed to ./run.sh --jitconfig; no registration token is
// involved and the runner auto-deregisters after one job.
func (c *client) GenerateJITConfig(ctx context.Context, org string, req JITRequest) (JITConfig, error) {
	r := &gh.GenerateJITConfigRequest{
		Name:          req.Name,
		RunnerGroupID: req.GroupID,
		Labels:        req.Labels,
	}
	if req.WorkFolder != "" {
		r.WorkFolder = gh.Ptr(req.WorkFolder)
	}
	cfg, _, err := c.gh.Actions.GenerateOrgJITConfig(ctx, org, r)
	if err != nil {
		return JITConfig{}, err
	}
	return JITConfig{
		RunnerID:         cfg.GetRunner().GetID(),
		RunnerName:       cfg.GetRunner().GetName(),
		EncodedJITConfig: cfg.GetEncodedJITConfig(),
	}, nil
}

// CreateRegistrationToken mints a ~1h registration token for config.sh --token
// (persistent runners). Mint on demand; never cache or log.
func (c *client) CreateRegistrationToken(ctx context.Context, org string) (Token, error) {
	t, _, err := c.gh.Actions.CreateOrganizationRegistrationToken(ctx, org)
	if err != nil {
		return Token{}, err
	}
	return Token{Value: t.GetToken(), ExpiresAt: t.GetExpiresAt().Time}, nil
}

// CreateRemoveToken mints a ~1h token for config.sh remove --token.
func (c *client) CreateRemoveToken(ctx context.Context, org string) (Token, error) {
	t, _, err := c.gh.Actions.CreateOrganizationRemoveToken(ctx, org)
	if err != nil {
		return Token{}, err
	}
	return Token{Value: t.GetToken(), ExpiresAt: t.GetExpiresAt().Time}, nil
}

// ListRunnerDownloads returns the runner application downloads (with sha256)
// for the org, an alternative to resolving actions/runner releases directly.
func (c *client) ListRunnerDownloads(ctx context.Context, org string) ([]Download, error) {
	ds, _, err := c.gh.Actions.ListOrganizationRunnerApplicationDownloads(ctx, org)
	if err != nil {
		return nil, err
	}
	out := make([]Download, 0, len(ds))
	for _, d := range ds {
		out = append(out, Download{
			OS:       d.GetOS(),
			Arch:     d.GetArchitecture(),
			URL:      d.GetDownloadURL(),
			Filename: d.GetFilename(),
			SHA256:   d.GetSHA256Checksum(),
		})
	}
	return out, nil
}
