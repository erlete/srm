package tui

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

// Control-plane operations, expressed as opSpecs for the generalized op-runner.
// The flagship is upgrade, which streams per-runner results live via the new
// service channel (decision #6); the rest run a single host-wide service call and
// map its results into the per-item panel. The launch helpers (root.go) apply the
// safety ladder: refresh/rollback/recreate confirm; upgrade/prune/provision show a
// dry-run preview first.

// upgradeApplyOp upgrades agents (serial, self-test + rollback) streaming each
// result the moment it lands. Used for both forward upgrade and rollback.
func upgradeApplyOp(opts service.UpgradeOpts, title string) opSpec {
	return opSpec{
		Title: title,
		Noun:  "runner",
		Run: func(ctx context.Context, mgr *service.Manager, prog chan<- core.ProgressEvent) ([]opItem, error) {
			ch := make(chan service.UpgradeResult)
			done := make(chan struct{})
			go func() {
				i := 0
				for r := range ch {
					i++
					if prog != nil {
						prog <- core.ProgressEvent{Index: i, Message: upgradeMsg(r)}
					}
				}
				close(done)
			}()
			results, errs := mgr.UpgradeLocalRunnersStream(ctx, opts, ch)
			close(ch)
			<-done
			return upgradeItems(results), firstErr(errs)
		},
	}
}

// planUpgradeCmd runs a dry-run upgrade pass and builds the preview + apply op.
// only (non-empty) scopes the upgrade to a runner selection; when empty the
// upgrade sweeps every local runner in scope, so its Apply is gated behind a
// typed hostname confirm (LOCKED spec decision #5).
func planUpgradeCmd(ctx context.Context, mgr *service.Manager, org string, only map[string]bool, t Theme) tea.Cmd {
	return func() tea.Msg {
		results, errs := mgr.UpgradeLocalRunners(ctx, service.UpgradeOpts{OrgFilter: org, Only: only, DryRun: true})
		if e := firstErr(errs); e != nil {
			return previewMsg{err: e}
		}
		apply := upgradeApplyOp(service.UpgradeOpts{OrgFilter: org, Only: only}, "Upgrade agent")
		note := scopeNote(org) + " · serial · self-test + rollback per runner"
		token := ""
		if len(only) > 0 {
			note = fmt.Sprintf("%d selected runner(s) · serial · self-test + rollback per runner", len(only))
		} else {
			// Host-wide sweep: typed-confirm on Apply. Fail CLOSED - if the hostname
			// can't be read, still require a typed token so the strongest gate can
			// never be skipped by a hostname lookup failure (LOCKED spec decision #5).
			if h, err := os.Hostname(); err == nil && h != "" {
				token = h
			} else {
				token = "CONFIRM"
			}
		}
		return previewMsg{
			title:      "Upgrade agent (preview)",
			note:       note,
			lines:      upgradePlanLines(t, results),
			spec:       apply,
			typedToken: token,
		}
	}
}

// rollbackOp restores each runner to its recorded previous version (a deliberate
// downgrade). Confirm-gated (no dry-run preview - it is already a recovery action).
// only (non-empty) scopes it to a runner selection.
func rollbackOp(org string, only map[string]bool) opSpec {
	return upgradeApplyOp(service.UpgradeOpts{OrgFilter: org, Only: only, Rollback: true}, "Rollback agent")
}

// refreshOp re-applies the systemd drop-in to every local runner (skip busy).
// only (non-empty) scopes it to a runner selection.
func refreshOp(org string, only map[string]bool) opSpec {
	return opSpec{
		Title: "Refresh units",
		Noun:  "runner",
		Run: func(ctx context.Context, mgr *service.Manager, prog chan<- core.ProgressEvent) ([]opItem, error) {
			res, errs := mgr.RefreshLocalUnits(ctx, org, only)
			items := make([]opItem, 0, len(res))
			for i, r := range res {
				it := opItem{Label: r.Org + "/" + r.Name}
				switch {
				case r.Err != nil:
					it.Outcome, it.Detail = outcomeFail, r.Err.Error()
				case r.Skipped != "":
					it.Outcome, it.Detail = outcomeSkip, r.Skipped
				case r.Refreshed:
					it.Outcome, it.Detail = outcomeOK, "refreshed"
				}
				items = append(items, it)
				if prog != nil {
					prog <- core.ProgressEvent{Index: i + 1, Total: len(res), Message: "refreshing " + r.Name}
				}
			}
			return items, firstErr(errs)
		},
	}
}

// planPruneCmd runs a dry-run prune and builds the preview + apply op.
func planPruneCmd(ctx context.Context, mgr *service.Manager, days int) tea.Cmd {
	maxAge := time.Duration(days) * 24 * time.Hour
	return func() tea.Msg {
		stats, err := mgr.PruneDepCacheRun(ctx, maxAge, true)
		if err != nil {
			return previewMsg{err: err}
		}
		apply := opSpec{
			Title: "Prune dep-cache",
			Noun:  "cache",
			Run: func(ctx context.Context, mgr *service.Manager, _ chan<- core.ProgressEvent) ([]opItem, error) {
				st, err := mgr.PruneDepCacheRun(ctx, maxAge, false)
				if err != nil {
					return nil, err
				}
				return []opItem{{Label: st.Root, Outcome: outcomeOK, Detail: fmt.Sprintf("pruned %d files / %s", st.Files, humanBytes(st.Bytes))}}, nil
			},
		}
		line := fmt.Sprintf("would prune %d files / %s from %s (older than %dd)", stats.Files, humanBytes(stats.Bytes), stats.Root, days)
		return previewMsg{title: "Prune dep-cache (preview)", note: "host-local cache eviction", lines: []string{line}, spec: apply}
	}
}

// planProvisionCmd runs a host-drift check and builds the preview + apply op.
func planProvisionCmd(ctx context.Context, mgr *service.Manager, t Theme) tea.Cmd {
	return func() tea.Msg {
		man := mgr.HostManifest()
		missing, err := mgr.HostDrift(ctx, man)
		if err != nil {
			return previewMsg{err: err}
		}
		lines := make([]string, 0, len(missing))
		for _, item := range missing {
			lines = append(lines, t.Busy.Render("  + "+item))
		}
		apply := opSpec{
			Title: "Provision host",
			Noun:  "host",
			Run: func(ctx context.Context, mgr *service.Manager, _ chan<- core.ProgressEvent) ([]opItem, error) {
				if err := mgr.ProvisionHost(ctx, man); err != nil {
					return nil, err
				}
				return []opItem{{Label: "this host", Outcome: outcomeOK, Detail: fmt.Sprintf("applied manifest (%d item(s) were missing)", len(missing))}}, nil
			},
		}
		return previewMsg{title: "Provision host (preview)", note: "installs the configured host dependency manifest", lines: lines, spec: apply}
	}
}

// recreateRunnersOp destroys and re-creates each target persistent runner with the
// same name/labels/group (BACKLOG #2).
func recreateRunnersOp(targets []service.FusedRunner) opSpec {
	return opSpec{
		Title: "Recreate runner(s)",
		Noun:  "runner",
		Run: func(ctx context.Context, mgr *service.Manager, prog chan<- core.ProgressEvent) ([]opItem, error) {
			items := make([]opItem, 0, len(targets))
			for i, r := range targets {
				it := opItem{Label: r.Org + "/" + r.Runner.Name}
				if err := mgr.RecreateRunner(ctx, r.Org, r.Runner.Name, customLabels(r.Runner.Labels), r.Runner.GroupID); err != nil {
					it.Outcome, it.Detail = outcomeFail, err.Error()
				} else {
					it.Outcome, it.Detail = outcomeOK, "recreated"
				}
				items = append(items, it)
				if prog != nil {
					// Emit the completed count (i+1) AFTER the work so the bar reaches
					// 100% (a 0-based pre-work Index peaks at (n-1)/n); matches refreshOp.
					prog <- core.ProgressEvent{Index: i + 1, Total: len(targets), Message: "recreating " + r.Runner.Name}
				}
			}
			return items, nil
		},
	}
}

// recreateSlotsOp destroys and re-creates each target ephemeral slot with the same
// slot id/labels/group (also the supported way to refresh a lane's agent).
func recreateSlotsOp(targets []service.FusedSlot) opSpec {
	return opSpec{
		Title: "Recreate slot(s)",
		Noun:  "slot",
		Run: func(ctx context.Context, mgr *service.Manager, prog chan<- core.ProgressEvent) ([]opItem, error) {
			items := make([]opItem, 0, len(targets))
			for i, s := range targets {
				it := opItem{Label: s.Org + "/slot " + s.Slot}
				if err := mgr.RecreateEphemeralSlot(ctx, s.Org, s.Slot); err != nil {
					it.Outcome, it.Detail = outcomeFail, err.Error()
				} else {
					it.Outcome, it.Detail = outcomeOK, "recreated"
				}
				items = append(items, it)
				if prog != nil {
					prog <- core.ProgressEvent{Index: i + 1, Total: len(targets), Message: "recreating slot " + s.Slot}
				}
			}
			return items, nil
		},
	}
}

// destroyRunnersOp destroys/deregisters each target persistent runner (bulk delete).
func destroyRunnersOp(targets []service.FusedRunner) opSpec {
	return opSpec{
		Title: "Destroy runner(s)",
		Noun:  "runner",
		Run: func(ctx context.Context, mgr *service.Manager, prog chan<- core.ProgressEvent) ([]opItem, error) {
			items := make([]opItem, 0, len(targets))
			for i, r := range targets {
				it := opItem{Label: r.Org + "/" + r.Runner.Name}
				var err error
				if r.Local {
					err = mgr.DestroyRunner(ctx, r.Org, r.Runner.Name)
				} else {
					err = mgr.DeleteRunner(ctx, r.Org, r.Runner.ID)
				}
				if err != nil {
					it.Outcome, it.Detail = outcomeFail, err.Error()
				} else if r.Local {
					it.Outcome, it.Detail = outcomeOK, "destroyed"
				} else {
					it.Outcome, it.Detail = outcomeOK, "deregistered"
				}
				items = append(items, it)
				if prog != nil {
					prog <- core.ProgressEvent{Index: i + 1, Total: len(targets), Message: "destroying " + r.Runner.Name}
				}
			}
			return items, nil
		},
	}
}

// destroySlotsOp tears down each target ephemeral slot lane (bulk).
func destroySlotsOp(targets []service.FusedSlot) opSpec {
	return opSpec{
		Title: "Destroy slot(s)",
		Noun:  "slot",
		Run: func(ctx context.Context, mgr *service.Manager, prog chan<- core.ProgressEvent) ([]opItem, error) {
			items := make([]opItem, 0, len(targets))
			for i, s := range targets {
				it := opItem{Label: s.Org + "/slot " + s.Slot}
				if err := mgr.DestroyEphemeralSlot(ctx, s.Org, s.Slot); err != nil {
					it.Outcome, it.Detail = outcomeFail, err.Error()
				} else {
					it.Outcome, it.Detail = outcomeOK, "destroyed"
				}
				items = append(items, it)
				if prog != nil {
					prog <- core.ProgressEvent{Index: i + 1, Total: len(targets), Message: "destroying slot " + s.Slot}
				}
			}
			return items, nil
		},
	}
}

// upgradeItems maps upgrade results to op result rows.
func upgradeItems(results []service.UpgradeResult) []opItem {
	items := make([]opItem, 0, len(results))
	for _, r := range results {
		it := opItem{Label: upgradeLabel(r)}
		switch {
		case r.Err != nil:
			it.Outcome, it.Detail = outcomeFail, r.Err.Error()
		case r.RolledBack:
			it.Outcome, it.Detail = outcomeRollback, "self-test failed - rolled back to "+orUnknownV(r.From)
		case r.Skipped != "":
			it.Outcome, it.Detail = outcomeSkip, r.Skipped
		case r.Upgraded:
			it.Outcome, it.Detail = outcomeOK, orUnknownV(r.From)+" -> "+r.To
		default:
			it.Outcome, it.Detail = outcomeSkip, "no change"
		}
		items = append(items, it)
	}
	return items
}

// upgradePlanLines renders a dry-run upgrade plan for the preview.
func upgradePlanLines(t Theme, results []service.UpgradeResult) []string {
	lines := make([]string, 0, len(results))
	for _, r := range results {
		label := t.Crumb.Render(upgradeLabel(r))
		if r.Skipped != "" {
			lines = append(lines, label+t.Faint.Render("  skip: "+r.Skipped))
		} else {
			lines = append(lines, label+t.Faint.Render("  "+orUnknownV(r.From)+" -> "+r.To))
		}
	}
	return lines
}

func upgradeLabel(r service.UpgradeResult) string {
	if r.Kind == service.KindEphemeral {
		return r.Org + "/slot " + r.Name
	}
	return r.Org + "/" + r.Name
}

func upgradeMsg(r service.UpgradeResult) string {
	switch {
	case r.Upgraded:
		return "upgraded " + upgradeLabel(r)
	case r.RolledBack:
		return "rolled back " + upgradeLabel(r)
	case r.Skipped != "":
		return "skipped " + upgradeLabel(r)
	default:
		return upgradeLabel(r)
	}
}

func orUnknownV(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// customLabels drops the read-only labels GitHub assigns (self-hosted + OS + arch),
// leaving the custom tags to re-apply on recreate.
func customLabels(labels []core.Label) []string {
	var out []string
	for _, l := range labels {
		if l.ReadOnly {
			continue
		}
		out = append(out, l.Name)
	}
	return out
}
