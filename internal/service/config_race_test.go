package service

import (
	"sync"
	"testing"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/secrets"
)

// A concurrent UpsertOrg (wizard onboard) must not race a cfg.Orgs reader (a fleet
// load). Before the whole-pointer-swap fix, UpsertOrg's in-place append raced
// OrgNames' range over cfg.Orgs; `go test -race` flags that. This exercises the exact
// shape: many readers ranging OrgNames while a writer upserts.
func TestConfigSwapNoRace(t *testing.T) {
	m := New(&config.Config{Orgs: []config.OrgConfig{{Name: "seed"}}}, secrets.EnvStore{}, "")

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					_ = m.OrgNames() // ranges cfg.Orgs under the snapshot
					_, _ = m.Config().Org("seed")
				}
			}
		})
	}

	for i := range 500 {
		m.UpsertOrg(config.OrgConfig{Name: "acme", AppID: int64(i)})
	}
	close(stop)
	wg.Wait()

	// Sanity: the writer's effect is visible and consistent.
	if _, ok := m.Config().Org("acme"); !ok {
		t.Fatal("upserted org not present after the concurrent run")
	}
}
