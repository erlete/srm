package github

import (
	"context"
	"net/http"

	gh "github.com/google/go-github/v88/github"
)

// InstallationAccess summarizes an org App installation's repo-visibility posture:
// enough to explain why private repos may be invisible. It is read via the APP JWT
// (not the installation token), as it is app-level installation metadata.
type InstallationAccess struct {
	RepositorySelection string // "all" | "selected"
	SeesPrivateRepos    bool   // repository "metadata" permission granted?
}

// AppInstallationAccess reads the org installation's granted permissions and
// repository selection using an APP-JWT-authenticated client (auth.AppHTTPClient).
// Repository visibility is gated by the repository-level "metadata" permission
// (implicitly present whenever ANY repo permission is granted); an installation
// holding ONLY organization permissions - the minimum srm needs to register
// runners - sees only PUBLIC repos, which silently breaks the "selected
// repositories" group repo-picker for private repos. Observational; never mutates.
func AppInstallationAccess(ctx context.Context, appHTTP *http.Client, org string) (InstallationAccess, error) {
	c, err := gh.NewClient(gh.WithHTTPClient(appHTTP))
	if err != nil {
		return InstallationAccess{}, err
	}
	inst, _, err := c.Apps.GetOrganizationInstallation(ctx, org)
	if err != nil {
		return InstallationAccess{}, err
	}
	acc := InstallationAccess{RepositorySelection: inst.GetRepositorySelection()}
	if p := inst.Permissions; p != nil {
		acc.SeesPrivateRepos = p.GetMetadata() != ""
	}
	return acc, nil
}

// GetArtifactRetention reads the org's artifact-and-log retention policy.
// Requires the App/token to hold the "Actions policies" (org administration)
// permission, which is broader than runner registration - callers must handle
// a 403 by degrading to read-only/unavailable.
func (c *client) GetArtifactRetention(ctx context.Context, org string) (Retention, error) {
	p, _, err := c.gh.Actions.GetArtifactAndLogRetentionPeriodInOrganization(ctx, org)
	if err != nil {
		return Retention{}, err
	}
	return Retention{Days: p.GetDays(), MaxAllowedDays: p.GetMaximumAllowedDays()}, nil
}

// SetArtifactRetention sets the org's retention period in days. The change is
// NOT retroactive - it applies only to new artifacts/logs.
func (c *client) SetArtifactRetention(ctx context.Context, org string, days int) error {
	_, err := c.gh.Actions.UpdateArtifactAndLogRetentionPeriodInOrganization(ctx, org, gh.ArtifactPeriodOpt{Days: gh.Ptr(days)})
	return err
}
