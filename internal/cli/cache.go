package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage the host's shared build-tool caches (/opt/srm-cache)",
	}
	cmd.AddCommand(newCachePruneCmd())
	return cmd
}

func newCachePruneCmd() *cobra.Command {
	var maxAgeDays int
	c := &cobra.Command{
		Use:   "prune",
		Short: "Evict build-tool cache entries not accessed in N days (run as root on the host; honors --dry-run)",
		Long: "Frees disk by deleting dependency-cache files under /opt/srm-cache that haven't " +
			"been accessed within the retention window. The package managers re-fetch any pruned " +
			"entry on next use, so this is safe to run (e.g. from cron). The shared tool cache " +
			"(/opt/hostedtoolcache) is not touched. Use --dry-run to preview.",
		RunE: func(_ *cobra.Command, _ []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()

			days := maxAgeDays
			if days <= 0 {
				days = mgr.Config().CacheRetentionDays
			}
			if days <= 0 {
				days = 30
			}

			stats, err := mgr.PruneDepCache(context.Background(), time.Duration(days)*24*time.Hour)
			if err != nil {
				return err
			}
			verb := "pruned"
			if mgr.Config().DryRun {
				verb = "would prune"
			}
			fmt.Printf("%s %d file(s), %.1f MiB from %s (not accessed in >%d days)\n",
				verb, stats.Files, float64(stats.Bytes)/(1024*1024), stats.Root, days)
			return nil
		},
	}
	c.Flags().IntVar(&maxAgeDays, "max-age-days", 0,
		"evict entries not accessed in this many days (default: config cacheRetentionDays, else 30)")
	return c
}
