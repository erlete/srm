package github

import (
	"context"

	gh "github.com/google/go-github/v88/github"
)

// GetArtifactRetention reads the org's artifact-and-log retention policy.
// Requires the App/token to hold the "Actions policies" (org administration)
// permission, which is broader than runner registration — callers must handle
// a 403 by degrading to read-only/unavailable.
func (c *client) GetArtifactRetention(ctx context.Context, org string) (Retention, error) {
	p, _, err := c.gh.Actions.GetArtifactAndLogRetentionPeriodInOrganization(ctx, org)
	if err != nil {
		return Retention{}, err
	}
	return Retention{Days: p.GetDays(), MaxAllowedDays: p.GetMaximumAllowedDays()}, nil
}

// SetArtifactRetention sets the org's retention period in days. The change is
// NOT retroactive — it applies only to new artifacts/logs.
func (c *client) SetArtifactRetention(ctx context.Context, org string, days int) error {
	_, err := c.gh.Actions.UpdateArtifactAndLogRetentionPeriodInOrganization(ctx, org, gh.ArtifactPeriodOpt{Days: gh.Ptr(days)})
	return err
}
