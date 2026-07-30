package cli

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/erlete/srm/internal/service"
)

func newReconcileCmd() *cobra.Command {
	var fix, reapEphemeral bool
	c := &cobra.Command{
		Use:   "reconcile",
		Short: "Audit host vs GitHub runner state (drift + health); --fix repairs host-side drift",
		Long: "Deep host/fleet check. Compares this host's runner units against GitHub and " +
			"reports drift: orphan units (no GitHub entry), stale drop-ins (missing cache " +
			"env / resource limits / per-org user), stuck (dead or offline) runners, legacy " +
			"layouts, and GitHub runners with no unit here. Ephemeral slot lanes are a separate " +
			"family judged by host health. Also reports host disk + cache sizes and per-runner " +
			"memory vs cap. Read-only by default.\n\n" +
			"--fix repairs HOST-side drift only (refresh stale, restart stuck, remove orphan " +
			"units); busy runners are skipped and GitHub-side entries are NEVER deleted " +
			"(use `srm runners delete` for that). --reap-ephemeral is the one exception: it " +
			"deregisters OFFLINE, non-busy ephemeral (srm-eph-*) JIT ghosts left by crashed " +
			"cycles. Honors --dry-run. Use --org to scope.",
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
			rep, err := mgr.Reconcile(context.Background(), fix, reapEphemeral, flagOrg, mgr.DryRun())
			if err != nil {
				return err
			}
			return renderReconcile(rep)
		},
	}
	c.Flags().BoolVar(&fix, "fix", false, "repair host-side drift (refresh stale, restart stuck, remove orphan units)")
	c.Flags().BoolVar(&reapEphemeral, "reap-ephemeral", false, "deregister offline ephemeral (srm-eph-*) JIT ghosts on GitHub (honors --dry-run)")
	return c
}

func renderReconcile(rep service.ReconcileReport) error {
	for org, e := range rep.ListErrs {
		fmt.Printf("WARN %s: %v\n", org, e)
	}

	// Counts by class.
	counts := map[string]int{}
	for _, r := range rep.Runners {
		counts[r.Class]++
	}
	// Every drift class the DRIFT detail can show must appear here, or the STATE
	// totals silently under-count (dropin-newer / ephemeral-newer were missing, so a
	// newer-template host showed them in DRIFT but not in the summary tally).
	order := []string{
		service.ClassHealthy, service.ClassEphemeralSlot, service.ClassStaleDropIn, service.ClassStuck,
		service.ClassOrphanUnit, service.ClassEphemeralStuck, service.ClassDropInNewer, service.ClassEphemeralNewer,
		service.ClassLegacyFlat, service.ClassOrphanGitHub, service.ClassUnknown,
	}
	fmt.Println("RUNNER STATE")
	for _, c := range order {
		if counts[c] > 0 {
			fmt.Printf("  %-14s %d\n", c, counts[c])
		}
	}

	// Detail for everything that isn't healthy.
	var drift []service.RunnerState
	for _, r := range rep.Runners {
		// A healthy ephemeral lane is normal, not drift (like ClassHealthy).
		if r.Class != service.ClassHealthy && r.Class != service.ClassEphemeralSlot {
			drift = append(drift, r)
		}
	}
	if len(drift) > 0 {
		fmt.Println("\nDRIFT")
		for _, r := range drift {
			line := fmt.Sprintf("  %-14s %s/%s - %s", r.Class, r.Org, r.Name, r.Detail)
			if r.Fix != "" {
				line += "  [" + r.Fix + "]"
			}
			if r.FixErr != nil {
				line += fmt.Sprintf("  FIX FAILED: %v", r.FixErr)
			}
			fmt.Println(line)
		}
	}

	// Host health.
	fmt.Println("\nHOST HEALTH")
	for _, d := range rep.Disks {
		fmt.Printf("  disk %-24s %s used of %s (%d%%), %s free\n",
			d.Path, humanBytes(d.UsedBytes), humanBytes(d.TotalBytes), d.UsePct, humanBytes(d.AvailBytes))
	}
	for _, c := range rep.Caches {
		fmt.Printf("  %-34s %s\n", c.Label, humanBytes(c.Bytes))
	}
	// Aggregate slice headroom: the box-wide ceiling for all runners combined - the
	// single best early-warning number for host OOM (shown only in auto-cap mode).
	if rep.SliceMax > 0 {
		// "now" disambiguates this LIVE figure from the per-runner MEMORY (peak / cap)
		// block below; omit the percent entirely when current is unknown (-1) rather
		// than fabricating "(0%)".
		pct := ""
		if rep.SliceCurrent >= 0 {
			pct = fmt.Sprintf(" (%d%%)", rep.SliceCurrent*100/rep.SliceMax)
		}
		fmt.Printf("  %-34s now %s / %s%s\n", "slice mem (srm.slice, all runners)",
			humanBytes(rep.SliceCurrent), humanBytes(rep.SliceMax), pct)
	}

	// Per-runner memory headroom (only local runners with a cap, validates limits).
	var mem []service.RunnerState
	for _, r := range rep.Runners {
		if r.Local && r.MemMax > 0 {
			mem = append(mem, r)
		}
	}
	if len(mem) > 0 {
		sort.Slice(mem, func(i, j int) bool { return mem[i].MemPeak > mem[j].MemPeak })
		fmt.Println("\nMEMORY (peak / cap)")
		for _, r := range mem {
			peak := "?"
			if r.MemPeak >= 0 {
				peak = humanBytes(r.MemPeak)
			}
			pct := ""
			if r.MemPeak >= 0 && r.MemMax > 0 {
				pct = fmt.Sprintf(" (%d%%)", r.MemPeak*100/r.MemMax)
			}
			extra := ""
			if r.MemCur >= 0 {
				extra += "  now " + humanBytes(r.MemCur)
			}
			if r.OOMKills > 0 {
				extra += fmt.Sprintf("  ⚠ %d OOM-kill(s)", r.OOMKills)
			}
			fmt.Printf("  %-30s %s / %s%s%s\n", r.Org+"/"+r.Name, peak, humanBytes(r.MemMax), pct, extra)
		}
	}

	// OOM events: the direct per-runner kill counter (cgroup memory.events
	// oom_kill). A nonzero count is the smoking gun behind a cap that's too low or
	// too many concurrent jobs - the answer to the "box OOM'd" question.
	var oomed []service.RunnerState
	for _, r := range rep.Runners {
		if r.OOMKills > 0 {
			oomed = append(oomed, r)
		}
	}
	if len(oomed) > 0 {
		sort.Slice(oomed, func(i, j int) bool { return oomed[i].OOMKills > oomed[j].OOMKills })
		fmt.Println("\nOOM EVENTS (cgroup kills since unit start - raise the cap or cut concurrency)")
		for _, r := range oomed {
			fmt.Printf("  %-30s %d OOM-kill(s)\n", r.Org+"/"+r.Name, r.OOMKills)
		}
	}

	// Ephemeral ghosts handled by --reap-ephemeral (GitHub-side deregistration).
	if len(rep.Reaped) > 0 {
		fmt.Printf("\nREAPED EPHEMERAL GHOSTS (%d)\n", len(rep.Reaped))
		for _, r := range rep.Reaped {
			line := fmt.Sprintf("  %s/%s - %s", r.Org, r.Name, r.Fix)
			if r.FixErr != nil {
				line += fmt.Sprintf("  FAILED: %v", r.FixErr)
			}
			fmt.Println(line)
		}
	}

	if rep.Applied {
		fmt.Println("\n(--fix applied)")
	}
	// Surface repair / reap failures as a non-zero exit.
	for _, r := range rep.Runners {
		if r.FixErr != nil {
			return fmt.Errorf("one or more repairs failed")
		}
	}
	for _, r := range rep.Reaped {
		if r.FixErr != nil {
			return fmt.Errorf("one or more reaps failed")
		}
	}
	return nil
}

// humanBytes renders a byte count compactly (e.g. 1.2G). Negative = unknown.
func humanBytes(n int64) string {
	if n < 0 {
		return "?"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}
