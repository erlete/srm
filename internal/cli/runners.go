package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/service"
)

func newRunnersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runners",
		Short: "Create, list, and delete self-hosted runners",
	}
	cmd.AddCommand(newRunnersListCmd(), newRunnersCreateCmd(), newRunnersDeleteCmd(), newRunnersDestroyCmd(), newRunnersRefreshCmd(), newRunnersUpgradeCmd())
	return cmd
}

func newRunnersUpgradeCmd() *cobra.Command {
	var (
		toVersion string
		rollback  bool
		force     bool
	)
	c := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade the actions/runner agent on local runners in place, idle-only (run as root on the host)",
		Long: "Replaces the actions/runner agent binaries on THIS host's runners with a newer " +
			"release, in place. Persistent runners keep their registration (the same swap the " +
			"agent's own auto-update performs); ephemeral lanes are rebuilt between job cycles. " +
			"Upgrades run one runner at a time - each is self-tested back to active (or rolled " +
			"back to its prior version) before the next is touched, so at most one runner is " +
			"ever offline.\n\n" +
			"Busy runners and ephemeral lanes mid-cycle are skipped (re-run to catch them idle). " +
			"With no --to-version the target is runnerVersionPin, else the version GitHub " +
			"currently publishes; a target without a verifiable checksum is refused. Use --org " +
			"to scope to one org, --dry-run to preview, --rollback to restore the prior version, " +
			"and --force to re-install or allow a downgrade.",
		RunE: func(_ *cobra.Command, _ []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			if err := requireOrgs(mgr); err != nil {
				return err
			}
			if flagOrg != "" {
				if _, ok := mgr.Config().Org(flagOrg); !ok {
					return fmt.Errorf("org %q not configured", flagOrg)
				}
			}
			if rollback && toVersion != "" {
				return fmt.Errorf("--rollback restores each runner's recorded previous version; it cannot be combined with --to-version")
			}

			results, errs := mgr.UpgradeLocalRunners(context.Background(), service.UpgradeOpts{
				OrgFilter: flagOrg,
				ToVersion: toVersion,
				DryRun:    mgr.Config().DryRun,
				Force:     force,
				Rollback:  rollback,
			})
			// A corrupt manifest / bad arg aborts the whole pass under a sentinel key.
			if e := errs["_state"]; e != nil {
				return e
			}
			if e := errs["_arg"]; e != nil {
				return e
			}
			for _, org := range mgr.OrgNames() {
				if e := errs[org]; e != nil {
					fmt.Printf("WARN %s: %v\n", org, e)
				}
			}
			if e := errs["_ephemeral"]; e != nil {
				fmt.Printf("WARN ephemeral: %v\n", e)
			}

			var upgraded, failed int
			for _, r := range results {
				switch {
				case r.Err != nil:
					failed++
					tag := ""
					if r.RolledBack {
						tag = " (rolled back to " + r.From + ")"
					}
					fmt.Printf("FAIL %s %s/%s: %v%s\n", r.Kind, r.Org, r.Name, r.Err, tag)
				case r.Skipped != "":
					fmt.Printf("skip %s %s/%s: %s\n", r.Kind, r.Org, r.Name, r.Skipped)
				case r.Upgraded:
					upgraded++
					fmt.Printf("ok   %s %s/%s: %s -> %s\n", r.Kind, r.Org, r.Name, orUnknownCLI(r.From), r.To)
				}
			}
			fmt.Printf("\nupgraded %d local runner(s)\n", upgraded)
			if failed > 0 {
				return fmt.Errorf("%d upgrade(s) failed", failed)
			}
			return nil
		},
	}
	c.Flags().StringVar(&toVersion, "to-version", "", "target agent version (default: runnerVersionPin, else the version GitHub publishes)")
	c.Flags().BoolVar(&rollback, "rollback", false, "restore each local runner to its recorded previous version")
	c.Flags().BoolVar(&force, "force", false, "re-install at the same version and allow a downgrade")
	return c
}

// orUnknownCLI renders an empty (unrecorded) version as "unknown" in command output.
func orUnknownCLI(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

func newRunnersCreateCmd() *cobra.Command {
	var (
		count      int
		namePrefix string
		labels     string
		group      string
		ephemeral  bool
	)
	c := &cobra.Command{
		Use:   "create",
		Short: "Provision self-hosted runners on THIS host - persistent, or --ephemeral JIT slots (run as root on the target)",
		RunE: func(_ *cobra.Command, _ []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			if err := requireOrgs(mgr); err != nil {
				return err
			}
			org, err := targetOrg(mgr)
			if err != nil {
				return err
			}

			// Ephemeral slots are numbered lanes (1..N) that mint a fresh JIT
			// registration per job - no name prefix, distinct from persistent runners.
			if ephemeral {
				if namePrefix != "" {
					return fmt.Errorf("--name-prefix is not used with --ephemeral (slots are numbered 1..N)")
				}
				spec := service.DeploySpec{Org: org, Count: count, Labels: splitCSV(labels), Group: group}
				slots, err := mgr.CreateEphemeralRunners(context.Background(), spec, nil)
				for _, s := range slots {
					fmt.Printf("ok   ephemeral slot %s\n", s)
				}
				if err != nil {
					return err
				}
				fmt.Printf("\ncreated %d ephemeral slot(s)\n", len(slots))
				return nil
			}

			if namePrefix == "" {
				return fmt.Errorf("--name-prefix is required")
			}
			spec := service.DeploySpec{
				Org:        org,
				Count:      count,
				NamePrefix: namePrefix,
				Labels:     splitCSV(labels),
				Group:      group,
			}
			names, err := mgr.CreateRunners(context.Background(), spec, nil)
			for _, n := range names {
				fmt.Printf("ok   %s\n", n)
			}
			if err != nil {
				return err
			}
			fmt.Printf("\ncreated %d runner(s)\n", len(names))
			return nil
		},
	}
	c.Flags().IntVar(&count, "count", 1, "number of runners (persistent) or slots (--ephemeral) to create")
	c.Flags().StringVar(&namePrefix, "name-prefix", "", "runner name prefix (persistent only; names become <prefix>-N)")
	c.Flags().StringVar(&labels, "labels", "", "comma-separated custom labels (tags)")
	c.Flags().StringVar(&group, "group", "", "runner group name (created if missing)")
	c.Flags().BoolVar(&ephemeral, "ephemeral", false, "create ephemeral JIT slots (one job per registration, clean slate) instead of persistent runners")
	return c
}

// newRunnerCycleCmd is the hidden worker invoked by an ephemeral slot's systemd
// unit (ExecStart=srm _runner-cycle --org X --slot N). It runs exactly one job
// cycle as root then exits; systemd re-execs it for the next job. Not for
// interactive use.
func newRunnerCycleCmd() *cobra.Command {
	var slot string
	c := &cobra.Command{
		Use:    "_runner-cycle",
		Hidden: true,
		Short:  "Internal: run one ephemeral job cycle (invoked by a slot's systemd unit)",
		RunE: func(_ *cobra.Command, _ []string) error {
			if flagOrg == "" || slot == "" {
				return fmt.Errorf("--org and --slot are required")
			}
			if flagDryRun {
				return fmt.Errorf("_runner-cycle runs a real job and does not support --dry-run")
			}
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			return mgr.RunCycle(context.Background(), flagOrg, slot)
		},
	}
	c.Flags().StringVar(&slot, "slot", "", "slot id")
	return c
}

func newRunnersListCmd() *cobra.Command {
	var currentOnly, remoteOnly bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List runners across all configured orgs (use --org to filter to one)",
		RunE: func(*cobra.Command, []string) error {
			if currentOnly && remoteOnly {
				return fmt.Errorf("--current-machine-only and --remote-machines-only are mutually exclusive")
			}
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			if err := requireOrgs(mgr); err != nil {
				return err
			}

			var rows []service.RunnerWithOrg
			if flagOrg != "" {
				// --org filters to a single, explicitly named org.
				rs, err := mgr.ListRunners(context.Background(), flagOrg)
				if err != nil {
					return err
				}
				for _, r := range rs {
					rows = append(rows, service.RunnerWithOrg{
						Org:    flagOrg,
						Runner: r,
						Local:  mgr.RunnerIsLocal(flagOrg, r.Name),
					})
				}
			} else {
				// Default: every configured org, with an ORG column.
				var errs map[string]error
				rows, errs = mgr.ListAllRunners(context.Background())
				for _, org := range mgr.OrgNames() {
					if e := errs[org]; e != nil {
						fmt.Printf("WARN %s: %v\n", org, e)
					}
				}
			}

			if currentOnly || remoteOnly {
				filtered := rows[:0]
				for _, row := range rows {
					if row.Local == currentOnly {
						filtered = append(filtered, row)
					}
				}
				rows = filtered
			}

			printRunnerRows(rows, mgr.LocalRunnerVersions())
			return nil
		},
	}
	c.Flags().BoolVar(&currentOnly, "current-machine-only", false, "only runners with an srm-managed tree on THIS host")
	c.Flags().BoolVar(&remoteOnly, "remote-machines-only", false, "only runners NOT installed by srm on this host")
	return c
}

// printRunnerRows renders runners with ORG + VERSION + MACHINE columns and a
// summary. versions maps RunnerVersionKey(org,name) to the recorded agent version
// for runners this host installed; a runner with no recorded version (remote, or
// created by an older srm) shows "-".
func printRunnerRows(rows []service.RunnerWithOrg, versions map[string]string) {
	fmt.Printf("%-16s %-12s %-30s %-8s %-5s %-8s %-9s %-7s %s\n", "ORG", "ID", "NAME", "STATUS", "JOB", "OS", "VERSION", "MACHINE", "LABELS")
	counts := make(map[string]int)
	var order []string
	local := 0
	for _, row := range rows {
		r := row.Runner
		job := "idle"
		if r.Busy {
			job = "busy"
		}
		machine := "remote"
		if row.Local {
			machine = "this"
			local++
		}
		ver := versions[service.RunnerVersionKey(row.Org, r.Name)]
		if ver == "" {
			ver = "-"
		}
		fmt.Printf("%-16s %-12d %-30s %-8s %-5s %-8s %-9s %-7s %s\n",
			row.Org, r.ID, r.Name, r.Status, job, r.OS, ver, machine, labelNames(r.Labels))
		if _, seen := counts[row.Org]; !seen {
			order = append(order, row.Org)
		}
		counts[row.Org]++
	}
	summary := fmt.Sprintf("%d runner(s)", len(rows))
	if len(order) > 1 {
		parts := make([]string, 0, len(order))
		for _, org := range order {
			parts = append(parts, fmt.Sprintf("%s=%d", org, counts[org]))
		}
		summary += fmt.Sprintf(" across %d orgs (%s)", len(order), strings.Join(parts, ", "))
	} else if len(order) == 1 {
		summary += " in " + order[0]
	}
	fmt.Printf("\n%s · %d on this host, %d remote\n", summary, local, len(rows)-local)
}

func newRunnersDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>...",
		Short: "Delete runners by id across any configured org (honors --dry-run; --org forces one org)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			if err := requireOrgs(mgr); err != nil {
				return err
			}

			ids := make([]int64, 0, len(args))
			for _, a := range args {
				id, err := strconv.ParseInt(a, 10, 64)
				if err != nil {
					return fmt.Errorf("invalid runner id %q: %w", a, err)
				}
				ids = append(ids, id)
			}

			ctx := context.Background()
			var failed int

			if flagOrg != "" {
				// Explicit single-org delete.
				failed += printDeleteResults(flagOrg, mgr.BulkDelete(ctx, flagOrg, ids, nil))
			} else {
				// Resolve each id to its owning org so cross-org `runners list`
				// ids delete from the RIGHT org (GitHub silently no-ops a delete
				// for an id that isn't in the targeted org).
				owned, unknown, errs := mgr.ResolveRunnerOrgs(ctx, ids)
				for _, org := range mgr.OrgNames() {
					if e := errs[org]; e != nil {
						fmt.Printf("WARN %s: %v\n", org, e)
					}
				}
				for _, org := range mgr.OrgNames() {
					var oids []int64
					for _, id := range ids {
						if owned[id] == org {
							oids = append(oids, id)
						}
					}
					if len(oids) > 0 {
						failed += printDeleteResults(org, mgr.BulkDelete(ctx, org, oids, nil))
					}
				}
				for _, id := range unknown {
					failed++
					fmt.Printf("FAIL %d: not found in any configured org (use --org to force)\n", id)
				}
			}

			if failed > 0 {
				return fmt.Errorf("%d deletion(s) failed", failed)
			}
			return nil
		},
	}
}

// printDeleteResults prints one line per result tagged with its org and returns
// the number of failures.
func printDeleteResults(org string, results []core.DeleteResult) (failed int) {
	for _, r := range results {
		if r.Err != nil {
			failed++
			fmt.Printf("FAIL %d (%s): %v\n", r.ID, org, r.Err)
		} else {
			fmt.Printf("ok   %d (%s)\n", r.ID, org)
		}
	}
	return failed
}

func newRunnersDestroyCmd() *cobra.Command {
	var (
		ephemeral bool
		slot      string
	)
	c := &cobra.Command{
		Use:   "destroy [name]",
		Short: "Remove a persistent runner (by name) or an --ephemeral slot (by --slot) from THIS host and deregister it (run as root)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			if err := requireOrgs(mgr); err != nil {
				return err
			}
			ctx := context.Background()

			// Ephemeral slots are lanes addressed by org + slot id, never by runner
			// name - a deliberately separate path so the two natures can't be mixed up.
			if ephemeral {
				if len(args) != 0 {
					return fmt.Errorf("--ephemeral destroys a slot by --slot, not a runner name")
				}
				if slot == "" {
					return fmt.Errorf("--slot is required with --ephemeral")
				}
				org, err := targetOrg(mgr)
				if err != nil {
					return err
				}
				if err := mgr.DestroyEphemeralSlot(ctx, org, slot); err != nil {
					return err
				}
				fmt.Printf("destroyed ephemeral slot %s (%s)\n", slot, org)
				return nil
			}
			if slot != "" {
				return fmt.Errorf("--slot is only valid with --ephemeral")
			}
			if len(args) != 1 {
				return fmt.Errorf("a runner name is required (or use --ephemeral --slot N)")
			}
			name := args[0]

			// Resolve which org owns this runner name. Names are unique within an
			// org but can collide across orgs (e.g. temporal-1 in two orgs), so
			// destroy must never guess - require --org to disambiguate.
			org := flagOrg
			if org == "" {
				orgs, errs := mgr.FindRunnerOrgsByName(ctx, name)
				for _, o := range mgr.OrgNames() {
					if e := errs[o]; e != nil {
						fmt.Printf("WARN %s: %v\n", o, e)
					}
				}
				switch len(orgs) {
				case 0:
					return fmt.Errorf("no runner named %q in any configured org (use --org to force)", name)
				case 1:
					org = orgs[0]
				default:
					return fmt.Errorf("runner %q exists in multiple orgs (%s) - specify --org", name, strings.Join(orgs, ", "))
				}
			} else if _, ok := mgr.Config().Org(org); !ok {
				return fmt.Errorf("org %q not configured", org)
			}

			if err := mgr.DestroyRunner(ctx, org, name); err != nil {
				return err
			}
			fmt.Printf("destroyed %s (%s)\n", name, org)
			return nil
		},
	}
	c.Flags().BoolVar(&ephemeral, "ephemeral", false, "destroy an ephemeral slot lane (by --slot) instead of a persistent runner")
	c.Flags().StringVar(&slot, "slot", "", "ephemeral slot id (with --ephemeral)")
	return c
}

func newRunnersRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Re-apply the systemd drop-in (hardening + tool-cache env) to local runners and restart them (run as root on the host)",
		Long: "Re-writes each local runner's systemd drop-in so changes to the hardening " +
			"directives, the host build-tool cache env (npm/pnpm/go/...), resource limits, or " +
			"per-org isolation take effect, then restarts the runner. Busy runners are skipped. " +
			"Use --org to stage a change one org at a time. No recreate needed.",
		RunE: func(_ *cobra.Command, _ []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			if err := requireOrgs(mgr); err != nil {
				return err
			}
			if flagOrg != "" {
				if _, ok := mgr.Config().Org(flagOrg); !ok {
					return fmt.Errorf("org %q not configured", flagOrg)
				}
			}
			results, errs := mgr.RefreshLocalUnits(context.Background(), flagOrg)
			for _, org := range mgr.OrgNames() {
				if e := errs[org]; e != nil {
					fmt.Printf("WARN %s: %v\n", org, e)
				}
			}
			var ok, failed int
			for _, r := range results {
				switch {
				case r.Err != nil:
					failed++
					fmt.Printf("FAIL %s/%s: %v\n", r.Org, r.Name, r.Err)
				case r.Skipped != "":
					fmt.Printf("skip %s/%s: %s\n", r.Org, r.Name, r.Skipped)
				default:
					ok++
					fmt.Printf("ok   %s/%s\n", r.Org, r.Name)
				}
			}
			fmt.Printf("\nrefreshed %d local runner(s)\n", ok)
			if failed > 0 {
				return fmt.Errorf("%d refresh(es) failed", failed)
			}
			return nil
		},
	}
}

func labelNames(labels []core.Label) string {
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return strings.Join(names, ",")
}
