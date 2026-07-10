package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/service"
)

// The fleet snapshot powers the Persistent, Ephemeral, and Drift tabs from one
// load. The fast tier (loadFleetCmd) is always fired; the host tier
// (loadHostTierCmd) follows only when this box can run a reconcile pass (root on
// the host) and merges drift + memory + host health when it returns.

// loadFleetCmd fetches the always-available fast tier off the event loop.
func loadFleetCmd(ctx context.Context, mgr *service.Manager, orgFilter string) tea.Cmd {
	return func() tea.Msg {
		return fleetMsg{snap: mgr.FleetFast(ctx, orgFilter)}
	}
}

// loadHostTierCmd runs a read-only reconcile pass (root-and-on-host only) and
// returns it for merging. An error means host data is unavailable here - the fast
// tier still renders, host telemetry is marked unavailable, and host ops disable.
func loadHostTierCmd(ctx context.Context, mgr *service.Manager, orgFilter string) tea.Cmd {
	return func() tea.Msg {
		rep, err := mgr.Reconcile(ctx, false, false, orgFilter, false) // audit-only (no fix/reap): dry-run irrelevant
		return hostTierMsg{rep: rep, orgFilter: orgFilter, err: err}
	}
}

// autoTickCmd schedules the next auto-refresh tick.
func autoTickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return autoTickMsg{} })
}

// fleetTab reports whether a tab is backed by the fused fleet snapshot (so tab
// switches between them are instant and share one load).
func fleetTab(t tab) bool {
	return t == tabPersistent || t == tabEphemeral || t == tabDrift
}

// newerBanner is the cross-tab alert shown when any fleet row's on-disk template
// is a NEWER generation than this srm binary (repair authoritative-skips them).
// Purple/amber, not error-red. Dismissed with x for the session.
func (m Model) newerBanner() string {
	if m.bannerDismissed {
		return ""
	}
	n := m.snap.NewerOnDisk()
	if n == 0 {
		return ""
	}
	t := m.theme
	msg := fmt.Sprintf("⚠ NEWER TEMPLATE ON DISK - update the srm binary; %d row(s) skipped by repair", n)
	return t.Newer.Render(msg) + t.Faint.Render("   [x to dismiss]")
}

// fleetHeaderLines counts the banner rows rendered above the table on a fleet tab,
// so layout can shrink the table to fit them. (Host stats moved to the Health tab;
// only the cross-tab newer-template banner sits above the table now.)
func (m Model) fleetHeaderLines() int {
	if m.newerBanner() != "" {
		return 1
	}
	return 0
}

// fleetStatusSuffix is the footer segment describing auto-refresh + data freshness.
func (m Model) fleetStatusSuffix() string {
	t := m.theme
	var parts []string
	if m.autoOn {
		parts = append(parts, fmt.Sprintf("auto %s", m.autoEvery))
	} else {
		parts = append(parts, "auto off")
	}
	if !m.loadedAt.IsZero() {
		age := m.sinceLoad()
		seg := fmt.Sprintf("updated %ds ago", int(age.Seconds()))
		if age > 30*time.Second {
			parts = append(parts, t.Busy.Render(seg))
		} else {
			parts = append(parts, t.Faint.Render(seg))
		}
	}
	if !m.snap.HostTierAvailable {
		parts = append(parts, t.Faint.Render("host data unavailable"))
	}
	return strings.Join(parts, t.Faint.Render(" · "))
}

// shortPath trims a host path to its last element for the disk strip.
func shortPath(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 && i < len(p)-1 {
		return p[i+1:]
	}
	if p == "" {
		return "/"
	}
	return p
}

// shortCacheLabel compresses a cache label ("tool cache /opt/...") to its leading word.
func shortCacheLabel(label string) string {
	if i := strings.Index(label, " /"); i > 0 {
		return label[:i]
	}
	return label
}
