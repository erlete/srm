package service

import (
	"fmt"

	"github.com/erlete/srm/internal/core"
)

// ValidateLabels enforces GitHub's 1-100 custom-label constraint for JIT
// config generation.
func ValidateLabels(labels []string) error {
	if len(labels) < 1 {
		return fmt.Errorf("at least one custom label is required")
	}
	if len(labels) > 100 {
		return fmt.Errorf("too many labels: %d (max 100)", len(labels))
	}
	return nil
}

// PublicRepoRisk reports whether a group exposes runners to public repos, a
// fork-PR RCE vector. The UI should treat enabling it as confirm-with-warning.
func PublicRepoRisk(g core.Group) bool {
	return g.AllowsPublic
}

// OfflineRunners filters a runner list to those GitHub shows as offline -
// these never auto-prune for non-ephemeral runners, so they are the candidates
// for bulk-delete-offline.
func OfflineRunners(runners []core.Runner) []core.Runner {
	var out []core.Runner
	for _, r := range runners {
		if !r.Online() {
			out = append(out, r)
		}
	}
	return out
}
