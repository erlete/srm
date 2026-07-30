package tui

import (
	"fmt"
	"strings"

	huh "charm.land/huh/v2"

	"github.com/erlete/srm/internal/core"
)

// manifestForm edits the host dependency manifest (Config.Host) - the data-driven
// `host:` layer `srm provision` applies host-once (never per job). List fields are
// edited one entry per line. Setup scripts are split into pre-placed PATHS (editable
// one per line) and INLINE bodies (preserved verbatim), plus an optional new inline
// script authored right here (P4) so a script need not be pre-placed on the host.
type manifestForm struct {
	form        *huh.Form
	apt         string // one apt package per line
	scriptPaths string // one setup-script PATH per line
	seeds       string // one tool@version tool-cache seed per line
	cachePaths  string // one persistent cache path per line
	newInline   string // an optional new inline setup script (appended on save)

	inlineKept []string // existing inline script bodies, preserved verbatim
}

func newManifestForm(cur core.DependencyManifest) *manifestForm {
	var paths, inline []string
	for _, s := range cur.SetupScripts {
		if core.IsInlineScript(s) {
			inline = append(inline, s)
		} else {
			paths = append(paths, s)
		}
	}
	mf := &manifestForm{
		apt:         strings.Join(cur.AptPackages, "\n"),
		scriptPaths: strings.Join(paths, "\n"),
		seeds:       strings.Join(cur.ToolCacheSeeds, "\n"),
		cachePaths:  strings.Join(cur.PersistentCachePaths, "\n"),
		inlineKept:  inline,
	}

	inlineTitle := "New inline setup script (optional; materialized at provision)"
	if len(inline) > 0 {
		inlineTitle = fmt.Sprintf("New inline setup script (optional) - %d already stored are kept", len(inline))
	}

	mf.form = huh.NewForm(
		huh.NewGroup(
			huh.NewNote().Title("Host dependency manifest").Description("Applied by the Provision op / `srm provision`, host-once (never per job). One entry per line; blank lines are ignored."),
			huh.NewText().Title("apt packages (one per line)").Value(&mf.apt).Lines(4),
			huh.NewText().Title("setup script PATHS on the host (one per line)").Value(&mf.scriptPaths).Lines(3),
			huh.NewText().Title("tool-cache seeds - tool@version (one per line)").Value(&mf.seeds).Lines(3),
			huh.NewText().Title("persistent cache paths (one per line)").Value(&mf.cachePaths).Lines(3),
			huh.NewText().Title(inlineTitle).Value(&mf.newInline).Lines(5).CharLimit(1<<16),
		),
	).WithWidth(72)
	return mf
}

// manifest turns the edited fields back into a DependencyManifest, re-joining the
// edited script paths with the preserved inline bodies plus any newly authored one.
func (mf *manifestForm) manifest() core.DependencyManifest {
	scripts := splitManifestLines(mf.scriptPaths)
	scripts = append(scripts, mf.inlineKept...)
	if b := strings.TrimRight(mf.newInline, "\r\n"); strings.TrimSpace(b) != "" {
		scripts = append(scripts, b)
	}
	return core.DependencyManifest{
		AptPackages:          splitManifestLines(mf.apt),
		SetupScripts:         scripts,
		ToolCacheSeeds:       splitManifestLines(mf.seeds),
		PersistentCachePaths: splitManifestLines(mf.cachePaths),
	}
}

// splitManifestLines splits a textarea value into trimmed, non-empty lines.
func splitManifestLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			out = append(out, t)
		}
	}
	return out
}
