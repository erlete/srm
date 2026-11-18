package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	gh "github.com/google/go-github/v88/github"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
	ghub "github.com/erlete/srm/internal/github"
	"github.com/erlete/srm/internal/runner"
)

// guardClient fakes the two GitHub calls the slot guard makes.
type guardClient struct {
	ghub.Client
	runner    core.Runner
	getErr    error
	deleteErr error
	deleted   []int64
}

func (c *guardClient) GetRunner(context.Context, string, int64) (core.Runner, error) {
	return c.runner, c.getErr
}

func (c *guardClient) DeleteRunner(_ context.Context, _ string, id int64) error {
	c.deleted = append(c.deleted, id)
	return c.deleteErr
}

// guardOrch fakes the slot's recorded JIT runner id.
type guardOrch struct {
	runner.Orchestrator
	id int64
}

func (o guardOrch) PendingJIT(string, string) int64 { return o.id }

func notFound() error {
	return &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotFound}}
}

func TestReleaseSlotRunner(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name       string
		id         int64
		client     *guardClient // nil = GitHub unreachable
		force      bool
		wantErr    bool
		wantBusy   bool
		wantDelete bool
	}{
		{name: "no recorded runner: between cycles", id: 0, client: &guardClient{}},
		{name: "idle: deregistered", id: 7, client: &guardClient{runner: core.Runner{ID: 7}}, wantDelete: true},
		{name: "busy: refused, kept registered", id: 7, client: &guardClient{runner: core.Runner{ID: 7, Busy: true}}, wantErr: true, wantBusy: true},
		{name: "busy + force: deregistered", id: 7, client: &guardClient{runner: core.Runner{ID: 7, Busy: true}}, force: true, wantDelete: true},
		{name: "already gone on GitHub", id: 7, client: &guardClient{getErr: notFound(), deleteErr: notFound()}, wantDelete: true},
		{name: "state unreadable: refused", id: 7, client: &guardClient{getErr: boom}, wantErr: true},
		{name: "state unreadable + force", id: 7, client: &guardClient{getErr: boom}, force: true, wantDelete: true},
		{name: "deregistration refused", id: 7, client: &guardClient{runner: core.Runner{ID: 7}, deleteErr: boom}, wantErr: true, wantDelete: true},
		{name: "deregistration refused + force", id: 7, client: &guardClient{deleteErr: boom}, force: true, wantDelete: true},
		{name: "GitHub unreachable: refused", id: 7, wantErr: true},
		{name: "GitHub unreachable + force", id: 7, force: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// An unconfigured org makes m.client fail: the "unreachable" cases.
			m := &Manager{cfg: &config.Config{}, clients: map[string]ghub.Client{}}
			if tc.client != nil {
				m.clients["acme"] = tc.client
			}
			err := m.releaseSlotRunner(context.Background(), guardOrch{id: tc.id}, "acme", "3", tc.force)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if errors.Is(err, ErrSlotBusy) != tc.wantBusy {
				t.Errorf("errors.Is(err, ErrSlotBusy) = %v, want %v (err %v)", !tc.wantBusy, tc.wantBusy, err)
			}
			if tc.client != nil && (len(tc.client.deleted) > 0) != tc.wantDelete {
				t.Errorf("deleted %v, wantDelete %v", tc.client.deleted, tc.wantDelete)
			}
		})
	}
}

func TestRefreshResultLabel(t *testing.T) {
	if got := (RefreshResult{Org: "acme", Name: "build-1"}).Label(); got != "acme/build-1" {
		t.Errorf("persistent label %q", got)
	}
	if got := (RefreshResult{Org: "acme", Name: "3", Ephemeral: true}).Label(); got != "acme/slot 3" {
		t.Errorf("ephemeral label %q", got)
	}
}
