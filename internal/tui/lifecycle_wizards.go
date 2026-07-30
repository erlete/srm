package tui

import (
	"context"
	"fmt"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/erlete/srm/internal/service"
	"github.com/erlete/srm/internal/setup"
)

// Onboard + Restore lifecycle wizards. Both mutate the config on disk and then rebuild
// the Manager in place via reloadMsg (Manager.Reload keeps the *Manager pointer, so
// the Model sees the new config without re-wiring). reloadMsg also invalidates the
// cached fleet snapshot so the new org / restored config shows on the next fleet view.

// reloadMsg is the outcome of a config-mutating wizard: err is a hard failure; note
// is the status to show on success; warn renders the note in the error style (a soft
// warning, e.g. the org saved but its GitHub auth failed).
type reloadMsg struct {
	note string
	warn bool
	err  error
}

// backupsMsg carries the restorable archives found for the Restore picker.
type backupsMsg struct {
	backups []service.BackupInfo
	dir     string
	err     error
}

// loadBackupsCmd lists the config-dir backup archives off the event loop.
func loadBackupsCmd(mgr *service.Manager) tea.Cmd {
	return func() tea.Msg {
		dir := filepath.Dir(mgr.ConfigPath())
		b, err := service.ListBackups(dir)
		return backupsMsg{backups: b, dir: dir, err: err}
	}
}

// onboardSaveCmd stores a pasted App key (encrypted) when the form used paste mode,
// persists the newly added org, reloads the Manager, and validates the org's GitHub
// auth by listing its runners (0 counts as success). The key material never leaves
// this command (no logging).
func onboardSaveCmd(ctx context.Context, mgr *service.Manager, org string, fields setup.OrgFields, edit bool) tea.Cmd {
	verb := "onboarded"
	if edit {
		verb = "updated"
	}
	return func() tea.Msg {
		if fields.KeyMode == setup.KeyModePaste {
			if err := mgr.StoreOrgKey(org, fields.KeyPaste, fields.Passphrase); err != nil {
				return reloadMsg{err: fmt.Errorf("store App key: %w", err)}
			}
		}
		if err := mgr.SaveConfig(); err != nil {
			return reloadMsg{err: fmt.Errorf("save config: %w", err)}
		}
		if err := mgr.Reload(); err != nil {
			return reloadMsg{err: fmt.Errorf("reload config: %w", err)}
		}
		if _, authErr := mgr.ListRunners(ctx, org); authErr != nil {
			return reloadMsg{note: fmt.Sprintf("org %s saved, but GitHub auth failed: %v (fix the App creds, then refresh)", org, authErr), warn: true}
		}
		return reloadMsg{note: fmt.Sprintf("org %s %s and authenticated", org, verb)}
	}
}

// restoreCmd extracts a backup archive over the config dir, then reloads the Manager.
func restoreCmd(archive string, mgr *service.Manager) tea.Cmd {
	return func() tea.Msg {
		dir := filepath.Dir(mgr.ConfigPath())
		if err := service.RestoreConfigDir(archive, dir); err != nil {
			return reloadMsg{err: fmt.Errorf("restore %s: %w", filepath.Base(archive), err)}
		}
		if err := mgr.Reload(); err != nil {
			return reloadMsg{note: "restored " + filepath.Base(archive) + ", but reload failed: " + err.Error(), warn: true}
		}
		return reloadMsg{note: "restored " + filepath.Base(archive) + " and reloaded config"}
	}
}

// restorePicker is the modal cursor list of restorable config-dir archives (newest
// first). enter opens a confirm gate before overwriting the config dir.
type restorePicker struct {
	theme   Theme
	backups []service.BackupInfo
	cursor  int
}

func (p *restorePicker) move(d int) {
	p.cursor += d
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.cursor >= len(p.backups) {
		p.cursor = len(p.backups) - 1
	}
}

func (p restorePicker) selected() (service.BackupInfo, bool) {
	if p.cursor < 0 || p.cursor >= len(p.backups) {
		return service.BackupInfo{}, false
	}
	return p.backups[p.cursor], true
}

func (p restorePicker) view(width, height int) string {
	t := p.theme
	rows := []string{t.ModalT.Render("Restore a config backup"), ""}
	for i, b := range p.backups {
		marker := "  "
		name := b.Name
		if i == p.cursor {
			marker = t.Title.Render("> ")
			name = t.Title.Render(name)
		} else {
			name = t.Crumb.Render(name)
		}
		meta := t.Faint.Render(fmt.Sprintf("  %s · %s", humanBytes(b.Size), b.ModTime.Format("2006-01-02 15:04")))
		rows = append(rows, marker+name+meta)
	}
	rows = append(rows, "", t.Help.Render("↑/↓ choose · enter restore (overwrites config) · esc cancel"))
	box := t.Modal.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
