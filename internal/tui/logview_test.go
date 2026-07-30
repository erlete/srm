package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/secrets"
	"github.com/erlete/srm/internal/service"
)

// logModel builds a model with one local persistent runner and one ephemeral slot,
// sized and on the Persistent tab.
func logModel(t *testing.T) Model {
	t.Helper()
	cfg := &config.Config{Orgs: []config.OrgConfig{{Name: "acme"}}}
	mgr := service.New(cfg, secrets.EnvStore{}, "")
	m := New(context.Background(), mgr)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = tm.(Model)
	tm, _ = m.Update(fleetMsg{snap: service.FleetSnapshot{
		HostTierAvailable: true,
		Runners: []service.FusedRunner{{
			RunnerWithOrg: service.RunnerWithOrg{Org: "acme", Local: true, Runner: core.Runner{ID: 1, Name: "r-1", OS: "Linux", Status: "online"}},
		}},
		Slots: []service.FusedSlot{{EphemeralSlot: service.EphemeralSlot{Org: "acme", Slot: "1", Active: true, UnitOK: true}}},
	}})
	m = tm.(Model)
	m.tab = tabPersistent
	return m
}

// L is gated on an elevated on-host session: without hostCapable, opening logs is
// refused with a status hint and the panel stays closed.
func TestLogsGatedOnHost(t *testing.T) {
	m := logModel(t)
	m.hostCapable = false
	mm, _ := m.openLogs()
	m = mm.(Model)
	if m.logOpen {
		t.Fatal("logs panel opened without host capability")
	}
	if !m.stErr {
		t.Fatal("expected an error status when logs are refused off-host")
	}
}

// With host capability and a local runner selected, L opens the panel (snapshot,
// not following) and issues the fetch command; the panel renders without panicking.
func TestLogsOpenPersistent(t *testing.T) {
	m := logModel(t)
	m.hostCapable = true
	mm, cmd := m.openLogs()
	m = mm.(Model)
	if !m.logOpen {
		t.Fatal("logs panel did not open for a local runner")
	}
	if m.log.follow {
		t.Fatal("follow should start off")
	}
	if cmd == nil {
		t.Fatal("expected a snapshot fetch command")
	}
	if m.logSubject.isSlot || m.logSubject.name != "r-1" || m.logSubject.org != "acme" {
		t.Fatalf("subject = %+v, want persistent acme/r-1", m.logSubject)
	}
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view with logs panel open")
	}
}

// A snapshot for a since-changed subject (the panel switched targets) is ignored;
// a matching one is applied. Neither path panics.
func TestLogsStaleSubjectIgnored(t *testing.T) {
	m := logModel(t)
	m.hostCapable = true
	mm, _ := m.openLogs()
	m = mm.(Model)

	// Stale result (different runner) - must be a no-op, panel stays open.
	tm, _ := m.Update(logsMsg{subj: logSubject{org: "acme", name: "other"}, content: "STALE"})
	m = tm.(Model)
	if !m.logOpen {
		t.Fatal("a stale snapshot closed the panel")
	}

	// Matching result applies; when not following, no re-poll tick is scheduled.
	tm, cmd := m.Update(logsMsg{subj: m.logSubject, content: "current\nlogs"})
	m = tm.(Model)
	if cmd != nil {
		t.Fatal("a snapshot while not following must not schedule a tick")
	}
	if v := m.View(); v.Content == "" {
		t.Fatal("nil view after applying a matching snapshot")
	}
}

// The follow ticker only re-fetches while the panel is open and following.
func TestLogsFollowTickGuard(t *testing.T) {
	m := logModel(t)
	m.hostCapable = true
	mm, _ := m.openLogs()
	m = mm.(Model)

	// Not following: a tick is inert.
	if _, cmd := m.Update(logTickMsg{}); cmd != nil {
		t.Fatal("tick re-fetched while not following")
	}

	// Following: a tick issues a fetch.
	m.log.follow = true
	if _, cmd := m.Update(logTickMsg{}); cmd == nil {
		t.Fatal("tick did not re-fetch while following")
	}

	// Closed: even if follow is somehow set, a tick is inert.
	m.logOpen = false
	if _, cmd := m.Update(logTickMsg{}); cmd != nil {
		t.Fatal("tick re-fetched after the panel closed")
	}
}
