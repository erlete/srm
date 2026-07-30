package provision

import (
	"fmt"
	"os"

	"github.com/erlete/srm/internal/core"
)

// resolveSetupScript turns a DependencyManifest.SetupScripts entry into a runnable
// file path. An entry may be either a PATH to a pre-placed script on the host (the
// historical form) or an INLINE script body authored in the TUI/CLI manifest editor
// (P4) - carried in host.setupScripts with no config-schema change.
//
//   - An existing file is used as-is; cleanup is a no-op.
//   - Otherwise, if the entry looks like an inline body (core.IsInlineScript), it is
//     materialized to a fresh temp file with the given extension, executable by owner
//     only (0700, so job code can't tamper), and the caller MUST call cleanup once the
//     script has run.
//   - A non-existent entry that is not an inline body is a mistyped path: it errors,
//     preserving the historical "setup script X: not found" behavior.
func resolveSetupScript(entry, ext string) (path string, cleanup func(), err error) {
	noop := func() {}
	if _, statErr := os.Stat(entry); statErr == nil {
		return entry, noop, nil // an existing file path always wins
	}
	if !core.IsInlineScript(entry) {
		return "", noop, os.ErrNotExist
	}
	f, err := os.CreateTemp("", "srm-inline-*"+ext)
	if err != nil {
		return "", noop, err
	}
	name := f.Name()
	if _, err := f.WriteString(entry); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return "", noop, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", noop, err
	}
	if err := os.Chmod(name, 0o700); err != nil {
		_ = os.Remove(name)
		return "", noop, err
	}
	return name, func() { _ = os.Remove(name) }, nil
}

// scriptLabel is a short, log-safe identifier for a SetupScripts entry: the path
// for a file, or "(inline script)" for an inline body (so an error or log line never
// dumps a whole multi-line script).
func scriptLabel(entry string) string {
	if core.IsInlineScript(entry) || len(entry) > 80 {
		return "(inline script)"
	}
	return entry
}

// wrapScriptErr annotates a setup-script error with a log-safe label.
func wrapScriptErr(entry string, err error) error {
	return fmt.Errorf("setup script %s: %w", scriptLabel(entry), err)
}
