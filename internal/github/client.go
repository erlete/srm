// Package github is a thin adapter over google/go-github's ActionsService,
// exposing only the org-level runner, group, token, JIT, download, and
// retention calls the tool needs. It returns core domain types so upper layers
// never depend on the go-github wire structs. The interface is mockable for
// tests.
package github

import (
	"context"
	"net/http"
	"time"

	gh "github.com/google/go-github/v88/github"

	"github.com/erlete/srm/internal/core"
)

// JITRequest parameterizes a just-in-time runner config.
type JITRequest struct {
	Name       string
	GroupID    int64
	Labels     []string
	WorkFolder string
}

// JITConfig is the result of generating a JIT runner config.
type JITConfig struct {
	RunnerID         int64
	RunnerName       string
	EncodedJITConfig string // passed to ./run.sh --jitconfig
}

// Token is a short-lived registration or remove token (~1h). Never persisted.
type Token struct {
	Value     string
	ExpiresAt time.Time
}

// Download describes a runner application download for an os/arch.
type Download struct {
	OS, Arch, URL, Filename, SHA256 string
}

// Retention is the org artifact-and-log retention policy.
type Retention struct {
	Days           int
	MaxAllowedDays int
}

// Client is the org-scoped GitHub Actions surface the tool depends on.
type Client interface {
	ListRunners(ctx context.Context, org string) ([]core.Runner, error)
	GetRunner(ctx context.Context, org string, id int64) (core.Runner, error)
	DeleteRunner(ctx context.Context, org string, id int64) error
	ListGroups(ctx context.Context, org string) ([]core.Group, error)
	CreateGroup(ctx context.Context, org, name, visibility string) (core.Group, error)
	GenerateJITConfig(ctx context.Context, org string, req JITRequest) (JITConfig, error)
	CreateRegistrationToken(ctx context.Context, org string) (Token, error)
	CreateRemoveToken(ctx context.Context, org string) (Token, error)
	ListRunnerDownloads(ctx context.Context, org string) ([]Download, error)
	GetArtifactRetention(ctx context.Context, org string) (Retention, error)
	SetArtifactRetention(ctx context.Context, org string, days int) error
}

type client struct {
	gh *gh.Client
}

// New builds a Client from an authenticated HTTP client (see internal/auth).
func New(httpClient *http.Client) (Client, error) {
	c, err := gh.NewClient(gh.WithHTTPClient(httpClient))
	if err != nil {
		return nil, err
	}
	return &client{gh: c}, nil
}

func toCoreRunner(r *gh.Runner) core.Runner {
	cr := core.Runner{
		ID:     r.GetID(),
		Name:   r.GetName(),
		OS:     r.GetOS(),
		Status: r.GetStatus(),
		Busy:   r.GetBusy(),
	}
	for _, l := range r.GetLabels() {
		cr.Labels = append(cr.Labels, core.Label{
			Name:     l.GetName(),
			ReadOnly: l.GetType() == "read-only",
		})
	}
	return cr
}

func toCoreGroup(g *gh.RunnerGroup) core.Group {
	return core.Group{
		ID:           g.GetID(),
		Name:         g.GetName(),
		Visibility:   g.GetVisibility(),
		Default:      g.GetDefault(),
		AllowsPublic: g.GetAllowsPublicRepositories(),
	}
}
