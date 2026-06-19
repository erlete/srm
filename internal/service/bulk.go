package service

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/erlete/srm/internal/core"
)

// BulkDelete deletes many runners with bounded concurrency (there is no bulk
// API, so this is N requests kept under the secondary rate limit). It never
// cancels siblings on failure: each runner's outcome is recorded. If progress
// is non-nil it receives one event per completed item; the channel is NOT
// closed here (the caller owns its lifecycle). Honors dry-run.
func (m *Manager) BulkDelete(ctx context.Context, org string, ids []int64, progress chan<- core.ProgressEvent) []core.DeleteResult {
	results := make([]core.DeleteResult, len(ids))

	org, err := m.requireOrg(org)
	if err != nil {
		for i, id := range ids {
			results[i] = core.DeleteResult{ID: id, Err: err}
		}
		return results
	}

	c, err := m.client(ctx, org)
	if err != nil {
		for i, id := range ids {
			results[i] = core.DeleteResult{ID: id, Err: err}
		}
		return results
	}

	dryRun := m.cfg.DryRun
	var (
		mu   sync.Mutex
		done int
	)
	report := func(i int, id int64, derr error) {
		results[i] = core.DeleteResult{ID: id, Err: derr}
		if progress == nil {
			return
		}
		mu.Lock()
		done++
		d := done
		mu.Unlock()
		msg := fmt.Sprintf("deleted runner %d", id)
		switch {
		case dryRun:
			msg = fmt.Sprintf("[dry-run] would delete runner %d", id)
		case derr != nil:
			msg = fmt.Sprintf("failed runner %d: %v", id, derr)
		}
		progress <- core.ProgressEvent{Index: d, Total: len(ids), Message: msg, Err: derr}
	}

	g := new(errgroup.Group)
	g.SetLimit(m.cfg.Concurrency)
	for i, id := range ids {
		i, id := i, id
		g.Go(func() error {
			var derr error
			if !dryRun {
				derr = c.DeleteRunner(ctx, org, id)
			}
			report(i, id, derr)
			return nil // never cancel siblings; per-item errors live in results
		})
	}
	_ = g.Wait()
	return results
}

// BulkCreate provisions multiple runners under a spec. Full implementation
// (mint JIT config -> download/extract agent -> systemd-supervised run loop)
// arrives with the runner-orchestration layer; see internal/runner and
// internal/service/lifecycle.go.
func (m *Manager) BulkCreate(ctx context.Context, spec core.CreateSpec, progress chan<- core.ProgressEvent) error {
	return fmt.Errorf("BulkCreate not yet implemented (pending runner orchestration layer)")
}
