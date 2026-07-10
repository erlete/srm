package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/secrets"
	"github.com/erlete/srm/internal/service"
)

// pressing i on a runner tab must open the Information panel AND render the
// subject's content (name + the runs-on snippet), not just leave the view non-empty.
func TestInfoKeyOpensAndRenders(t *testing.T) {
	cfg := &config.Config{Orgs: []config.OrgConfig{{Name: "acme"}}}
	mgr := service.New(cfg, secrets.EnvStore{}, "")
	m := New(context.Background(), mgr)

	tm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = tm.(Model)

	snap := service.FleetSnapshot{
		Runners: []service.FusedRunner{{
			RunnerWithOrg: service.RunnerWithOrg{Org: "acme", Local: true, Runner: core.Runner{ID: 7, Name: "armora-1", OS: "Linux", Status: "online", Labels: []core.Label{{Name: "self-hosted", ReadOnly: true}, {Name: "temporal"}}}},
			GroupName:     "srm-ci", AgentVersion: "2.335.1", Published: "2.335.1",
		}},
	}
	tm, _ = m.Update(fleetMsg{snap: snap})
	m = tm.(Model)
	m.tab = tabPersistent

	tm, _ = m.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	m = tm.(Model)
	if !m.infoOpen {
		t.Fatal("pressing i did not open the Information panel")
	}
	v := m.View()
	for _, want := range []string{"armora-1", "Use in a workflow", "runs-on", "temporal", "srm-ci"} {
		if !strings.Contains(v.Content, want) {
			t.Errorf("info view missing %q", want)
		}
	}

	// esc closes it.
	tm, _ = m.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	m = tm.(Model)
	if m.infoOpen {
		t.Error("pressing i again should close the panel")
	}
}
