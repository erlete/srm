package cli

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/erlete/srm/internal/joblog"
)

// newRunsCmd surfaces the durable ephemeral job-log store (internal/joblog): the runs
// srm captured before each ephemeral wipe, and their logs. It is the CLI mirror of the
// (Phase D) Runs screen; the store is root-owned, so these commands run elevated on the
// host. Metadata is what srm knows locally (runner/slot/org/timing/result); repo +
// workflow enrichment via GitHub is the Runs screen's job.
func newRunsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "runs",
		Short: "List captured ephemeral job runs and view their durable logs",
	}
	c.AddCommand(newRunsListCmd(), newRunsLogsCmd())
	return c
}

func newRunsListCmd() *cobra.Command {
	var slot string
	var limit int
	c := &cobra.Command{
		Use:   "list",
		Short: "List captured ephemeral job runs, newest first (--org, --slot, --limit)",
		RunE: func(_ *cobra.Command, _ []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()

			all, err := mgr.JobLogStore().List()
			if err != nil {
				return fmt.Errorf("read job-log store (run elevated on the host): %w", err)
			}
			var rows []joblog.Meta
			for _, m := range all {
				if flagOrg != "" && m.Org != flagOrg {
					continue
				}
				if slot != "" && m.Slot != slot {
					continue
				}
				rows = append(rows, m)
				if limit > 0 && len(rows) >= limit {
					break
				}
			}
			if len(rows) == 0 {
				fmt.Printf("no captured runs at %s\n", mgr.JobLogStore().Root)
				return nil
			}
			fmt.Printf("%-12s %-16s %-6s %-7s %-19s %-9s %-4s %s\n",
				"RUNNER-ID", "ORG", "SLOT", "RESULT", "STARTED", "DURATION", "LOG", "RUNNER")
			for _, m := range rows {
				fmt.Printf("%-12d %-16s %-6s %-7s %-19s %-9s %-4s %s\n",
					m.RunnerID, m.Org, m.Slot, runResult(m), startedStr(m),
					m.Duration().Round(time.Second), yesNo(m.HasLog), m.RunnerName)
			}
			return nil
		},
	}
	c.Flags().StringVar(&slot, "slot", "", "filter to one ephemeral slot")
	c.Flags().IntVar(&limit, "limit", 0, "show at most N runs (0 = all)")
	return c
}

func newRunsLogsCmd() *cobra.Command {
	var slot string
	c := &cobra.Command{
		Use:   "logs <runner-id>",
		Short: "Print the captured log for a run by its GitHub runner id (--org/--slot to disambiguate)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("runner id must be a number, got %q", args[0])
			}
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()

			all, err := mgr.JobLogStore().List()
			if err != nil {
				return fmt.Errorf("read job-log store (run elevated on the host): %w", err)
			}
			var matches []joblog.Meta
			for _, m := range all {
				if m.RunnerID != id {
					continue
				}
				if flagOrg != "" && m.Org != flagOrg {
					continue
				}
				if slot != "" && m.Slot != slot {
					continue
				}
				matches = append(matches, m)
			}
			switch len(matches) {
			case 0:
				return fmt.Errorf("no captured run with runner id %d (try `srm runs list`)", id)
			case 1:
			default:
				return fmt.Errorf("runner id %d matches %d runs across orgs/slots - narrow with --org/--slot", id, len(matches))
			}
			m := matches[0]
			if !m.HasLog {
				fmt.Fprintf(os.Stderr, "srm: no log was captured for runner %d (%s/%s) - the job may have produced none\n", id, m.Org, m.Slot)
				return nil
			}
			b, err := mgr.JobLogStore().ReadLog(m)
			if err != nil {
				return fmt.Errorf("read log: %w", err)
			}
			_, err = os.Stdout.Write(b)
			return err
		},
	}
	c.Flags().StringVar(&slot, "slot", "", "disambiguate by ephemeral slot")
	return c
}

func runResult(m joblog.Meta) string {
	if m.OK {
		return "ok"
	}
	return "FAIL"
}

func startedStr(m joblog.Meta) string {
	if m.StartedUnix == 0 {
		return "-"
	}
	return m.Started().Format("2006-01-02 15:04:05")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "-"
}
