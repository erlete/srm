package tui

import (
	"context"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

// Lifecycle-tab operation plumbing: backup (op-runner), and the guarded uninstall
// (dry-run blast-radius preview -> typed hostname confirm -> apply).

// backupOp snapshots the config dir to a timestamped tarball.
func backupOp() opSpec {
	return opSpec{
		Title: "Back up config",
		Noun:  "backup",
		Run: func(ctx context.Context, mgr *service.Manager, _ chan<- core.ProgressEvent) ([]opItem, error) {
			path, err := mgr.BackupConfig()
			if err != nil {
				return nil, err
			}
			return []opItem{{Label: "config", Outcome: outcomeOK, Detail: "wrote " + path}}, nil
		},
	}
}

// uninstallPreviewCmd runs a dry-run uninstall and builds the blast-radius preview
// plus the apply op, gated behind a typed hostname confirm. opts carries the scope
// and the Keep*/Force/Purge toggles from the Step-1 form. When the opts would remove
// /etc/srm it escalates: a writable-backup pre-check refuses up front, and Apply
// chains a second "PURGE" typed-confirm after the hostname one.
func uninstallPreviewCmd(ctx context.Context, mgr *service.Manager, t Theme, opts service.UninstallOpts) tea.Cmd {
	return func() tea.Msg {
		// Gate 3 (up front): a purge that removes /etc/srm must have a writable backup
		// dir, else the real teardown would abort mid-way to protect the App keys.
		if err := mgr.EnsureBackupDirWritable(opts); err != nil {
			return previewMsg{err: err}
		}
		rep, err := mgr.UninstallPreview(ctx, opts)
		if err != nil {
			return previewMsg{err: err}
		}
		host, _ := os.Hostname()
		if host == "" {
			host = "this-host"
		}
		apply := opSpec{
			Title: "Uninstall",
			Noun:  "artifact",
			Run: func(ctx context.Context, mgr *service.Manager, prog chan<- core.ProgressEvent) ([]opItem, error) {
				if prog != nil {
					prog <- core.ProgressEvent{Message: "tearing down srm artifacts…"}
				}
				rep, err := mgr.UninstallApply(ctx, opts)
				if err != nil {
					return nil, err
				}
				return uninstallResultItems(rep), nil
			},
		}
		scope := "FULL HOST"
		if opts.Org != "" {
			scope = "org " + opts.Org
		}
		note := "GitHub-side runners are deregistered unless you kept them"
		token2 := ""
		if opts.PurgeRemovesConfig() {
			note = "PURGE: /etc/srm (config + App keys) is backed up, then DELETED · double-confirm required"
			token2 = "PURGE"
		}
		return previewMsg{
			title:       "Uninstall blast radius (" + scope + ")",
			note:        note,
			lines:       uninstallBlastLines(t, rep),
			spec:        apply,
			typedToken:  host,
			typedToken2: token2,
		}
	}
}

// uninstallBlastLines renders the dry-run report as a color-coded blast radius.
func uninstallBlastLines(t Theme, rep service.UninstallReport) []string {
	var lines []string
	add := func(style func(...string) string, prefix string, items []string) {
		for _, it := range items {
			lines = append(lines, style(prefix+it))
		}
	}
	red := func(s ...string) string { return t.Offline.Render(s[0]) }
	yellow := func(s ...string) string { return t.Busy.Render(s[0]) }
	for _, r := range rep.Persistent {
		add(red, "runner  ", []string{r})
	}
	for _, e := range rep.Ephemeral {
		add(red, "slot    ", []string{e})
	}
	for _, u := range rep.Users {
		add(red, "user    ", []string{u})
	}
	for _, p := range rep.Paths {
		add(red, "path    ", []string{p})
	}
	if rep.ConfigDir != "" {
		lines = append(lines, t.Offline.Render("config  "+rep.ConfigDir))
	}
	if rep.Binary != "" {
		lines = append(lines, t.Offline.Render("binary  "+rep.Binary))
	}
	for _, s := range rep.Skipped {
		add(yellow, "skip    ", []string{s})
	}
	if len(lines) == 0 {
		lines = append(lines, t.Faint.Render("nothing srm-attributable to remove"))
	}
	return lines
}

// uninstallResultItems maps an applied uninstall report to op result rows.
func uninstallResultItems(rep service.UninstallReport) []opItem {
	var items []opItem
	count := func(label string, n int) {
		if n > 0 {
			items = append(items, opItem{Label: label, Outcome: outcomeOK, Detail: fmt.Sprintf("%d removed", n)})
		}
	}
	count("runners", len(rep.Persistent))
	count("slots", len(rep.Ephemeral))
	count("users", len(rep.Users))
	count("paths", len(rep.Paths))
	if rep.BackupPath != "" {
		items = append(items, opItem{Label: "backup", Outcome: outcomeOK, Detail: rep.BackupPath})
	}
	for _, e := range rep.Errors {
		items = append(items, opItem{Label: "error", Outcome: outcomeFail, Detail: e})
	}
	if len(items) == 0 {
		items = append(items, opItem{Label: "uninstall", Outcome: outcomeOK, Detail: "nothing to remove"})
	}
	return items
}
