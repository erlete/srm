package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

// The Step-1 form maps its scope sentinel + selected option keys onto UninstallOpts;
// nothing selected is the fail-safe full-host teardown that keeps /etc/srm.
func TestUninstallFormOpts(t *testing.T) {
	// Default form: full-host scope, no toggles.
	def := newUninstallForm([]string{"acme"}, "").opts()
	if def.Org != "" || def.Purge || def.KeepConfig || def.KeepBinary || def.KeepGitHub || def.Force {
		t.Fatalf("default opts should be an empty full-host teardown, got %+v", def)
	}
	if def.PurgeRemovesConfig() {
		t.Error("default opts must not remove /etc/srm")
	}

	// A specific org preselects that scope.
	if got := newUninstallForm([]string{"acme"}, "acme").opts().Org; got != "acme" {
		t.Errorf("scoped form Org = %q, want acme", got)
	}

	// Every toggle selected maps through.
	uf := newUninstallForm([]string{"acme"}, "")
	uf.options = []string{uoPurge, uoKeepConfig, uoKeepBinary, uoKeepGitHub, uoForce}
	got := uf.opts()
	if !got.Purge || !got.KeepConfig || !got.KeepBinary || !got.KeepGitHub || !got.Force {
		t.Fatalf("all-toggles opts not mapped: %+v", got)
	}
	// Purge + KeepConfig means /etc/srm is kept, so the extra gates must not escalate.
	if got.PurgeRemovesConfig() {
		t.Error("purge with keep-config must not remove /etc/srm")
	}
}

// A full-host purge chains the two typed tokens: the hostname arms the first gate,
// which opens a second PURGE gate; only after PURGE does the op run.
func TestUninstallPurgeChainsSecondToken(t *testing.T) {
	m := sizedModel(t)
	spec := opSpec{Title: "Uninstall", Noun: "artifact", Run: func(context.Context, *service.Manager, chan<- core.ProgressEvent) ([]opItem, error) { return nil, nil }}

	tm, _ := m.Update(previewMsg{title: "x", note: "y", lines: []string{"z"}, spec: spec, typedToken: "myhost", typedToken2: "PURGE"})
	m = tm.(Model)
	if m.pendingTyped != "myhost" || m.pendingTyped2 != "PURGE" {
		t.Fatalf("preview did not carry both tokens: %q / %q", m.pendingTyped, m.pendingTyped2)
	}

	// Apply (y) -> the hostname typed-confirm.
	tm, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = tm.(Model)
	if !m.typedOpen || m.typed.want != "myhost" {
		t.Fatalf("apply should open the hostname gate, want=%q open=%v", m.typed.want, m.typedOpen)
	}

	// Correct hostname + enter -> chains to the PURGE gate, op still not running.
	m.typed.input.SetValue("myhost")
	tm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = tm.(Model)
	if !m.typedOpen || m.typed.want != "PURGE" {
		t.Fatalf("first match should chain to the PURGE gate, want=%q open=%v", m.typed.want, m.typedOpen)
	}
	if m.pendingTyped2 != "" {
		t.Error("pendingTyped2 should be consumed once the second gate opens")
	}
	if m.op.open {
		t.Fatal("op must NOT run before the PURGE token is entered")
	}

	// PURGE + enter -> the op finally runs.
	m.typed.input.SetValue("PURGE")
	tm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = tm.(Model)
	if m.typedOpen {
		t.Error("typed-confirm should close after PURGE")
	}
	if !m.op.open {
		t.Error("op should be running after both gates pass")
	}
}

// A non-purge uninstall keeps its single hostname gate and never chains.
func TestUninstallNoPurgeSingleGate(t *testing.T) {
	m := sizedModel(t)
	spec := opSpec{Title: "Uninstall", Noun: "artifact", Run: func(context.Context, *service.Manager, chan<- core.ProgressEvent) ([]opItem, error) { return nil, nil }}
	tm, _ := m.Update(previewMsg{title: "x", spec: spec, typedToken: "myhost"})
	m = tm.(Model)
	tm, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = tm.(Model)
	m.typed.input.SetValue("myhost")
	tm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = tm.(Model)
	if m.typedOpen {
		t.Error("a non-purge uninstall should run after the single hostname gate")
	}
	if !m.op.open {
		t.Error("op should run once the hostname is confirmed")
	}
}
