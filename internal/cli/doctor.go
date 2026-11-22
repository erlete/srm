package cli

import (
	"context"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/service"
)

// limitsLine renders the set cgroup directives of r as a compact one-liner for
// `srm doctor` (only non-empty fields, in drop-in order).
func limitsLine(r config.ResourceLimits) string {
	var parts []string
	for _, kv := range []struct{ k, v string }{
		{"MemoryHigh", r.MemoryHigh},
		{"MemoryMax", r.MemoryMax},
		{"MemorySwapMax", r.MemorySwapMax},
		{"CPUWeight", r.CPUWeight},
		{"TasksMax", r.TasksMax},
	} {
		if kv.v != "" {
			parts = append(parts, kv.k+"="+kv.v)
		}
	}
	return strings.Join(parts, " ")
}

// printCacheEnv renders an org's build-tool cache env for `srm doctor`: how many
// vars its runners get and where, the orgs[].cache.env changes (+added, ~re-pointed,
// -removed), and every entry srm ignores (left out of the unit, so it costs only
// that cache). Advisory: it never fails doctor.
func printCacheEnv(cfg *config.Config, org string) {
	root := cfg.CacheRootFor(org)
	if root == "" {
		root = config.DefaultCacheRoot
	}
	var ov map[string]string
	if oc, ok := cfg.Org(org); ok && oc != nil {
		ov = oc.Cache.Env
	}
	env := config.ToolCacheEnv(root, ov)
	line := fmt.Sprintf("%d vars under %s", len(env), root)
	if len(ov) > 0 {
		def := map[string]bool{}
		for _, e := range config.ToolCacheEnv(root, nil) {
			def[e.Key] = true
		}
		got := map[string]bool{}
		for _, e := range env {
			got[e.Key] = true
		}
		var changes []string
		for _, k := range slices.Sorted(maps.Keys(ov)) {
			switch {
			case def[k] && !got[k]:
				changes = append(changes, "-"+k)
			case def[k] && got[k]:
				changes = append(changes, "~"+k)
			case got[k]:
				changes = append(changes, "+"+k)
			}
		}
		if len(changes) > 0 {
			line += " (cache.env: " + strings.Join(changes, " ") + ")"
		}
	}
	fmt.Printf("cache env:   %s\n", line)
	for _, p := range config.CacheEnvProblems(ov) {
		fmt.Printf("             IGNORED cache.env %s\n", p)
	}
}

// printDinDReadiness renders the rootless-Docker host readiness report (gathered by
// the service, Linux-only). A disabled report prints nothing; otherwise it lists the
// cross-org caveat then each check as a padded `name  detail` line, matching the rest
// of `doctor`'s host section.
func printDinDReadiness(rep service.DinDReport) {
	if !rep.Enabled {
		return
	}
	fmt.Println()
	fmt.Println("rootless docker (docker.rootlessDinD on):")

	// Per-JOB clean-slate always holds (each job wipes its data-root). The cross-ORG
	// uid/data boundary, however, exists only under isolation.perOrgUsers.
	if rep.CrossOrgRisk {
		fmt.Println("  WARNING: isolation.perOrgUsers is off with >1 org - rootless DinD gives")
		fmt.Println("           per-JOB clean-slate but NO cross-ORG boundary (shared user/subuid/")
		fmt.Println("           data-root). Enable isolation.perOrgUsers for a cross-org boundary.")
	}
	for _, c := range rep.Checks {
		fmt.Printf("  %-18s %s\n", c.Name, c.Detail)
	}
	// FP1: a missing hard prerequisite fails DinD jobs mid-run (silently queuing the
	// fleet), so call it out as a BLOCKER with the one-command fix rather than leaving
	// it buried in the list above.
	if rep.HasBlocker() {
		fmt.Printf("  BLOCKER: %d prerequisite(s) missing - rootless-DinD jobs will queue/fail until fixed.\n", len(rep.Blockers()))
		fmt.Println("           Fix: `sudo srm provision --rootless-dind`")
	}
}

// printDinDPortRanges renders the rootless-DinD published-port partitioning (Linux
// only; gathered by the service). Nothing prints when rootless DinD is off. When no
// org partitions, a one-line hint replaces the per-slot table; otherwise every local
// DinD slot lists the block its org assigns it next to the range its running daemon
// actually uses (read from the slot's network namespace), then any warnings.
func printDinDPortRanges(rep service.DinDPortReport) {
	if !rep.Enabled {
		return
	}
	fmt.Println()
	fmt.Println("rootless docker published ports (orgs[].dind.portRange):")
	if !rep.AnyPartitioned() {
		fmt.Println("  not partitioned: every slot uses the default range 32768-60999 (set orgs[].dind.portRange to give each slot its own block)")
		return
	}
	for _, o := range rep.Orgs {
		switch {
		case !o.Partitioned:
			fmt.Printf("  %-18s not partitioned (default range 32768-60999)\n", o.Org)
		case o.Err != "":
			fmt.Printf("  %-18s INVALID: %s\n", o.Org, o.Err)
		default:
			fmt.Printf("  %-18s pool %s, %d ports per slot (room for %d slots)\n", o.Org, o.Pool, o.PerSlot, o.Capacity)
		}
	}
	if rep.WrapperProblem != "" {
		fmt.Printf("  %-18s %s (CANNOT RUN: %s)\n", "port wrapper", rep.WrapperPath, rep.WrapperProblem)
	} else {
		fmt.Printf("  %-18s %s\n", "port wrapper", rep.WrapperPath)
	}
	if len(rep.Slots) > 0 {
		fmt.Printf("  %-24s %-13s %-13s %-9s %s\n", "slot", "configured", "effective", "tw_reuse", "state")
		for _, s := range rep.Slots {
			want := "default"
			if s.Partitioned {
				want = "-"
				if s.WantErr == "" {
					want = fmt.Sprintf("%d-%d", s.WantLo, s.WantHi)
				}
			}
			eff, reuse := "-", "-"
			if s.EffErr == "" {
				eff, reuse = fmt.Sprintf("%d-%d", s.EffLo, s.EffHi), strconv.Itoa(s.EffTWReuse)
			}
			state := s.State
			switch s.State {
			case service.PortStatePending:
				state += " (applies on the slot's next cycle)"
			case service.PortStateNotApplied:
				state += ": " + s.WantErr
			case service.PortStateUnreadable:
				state += ": " + s.EffErr
			}
			fmt.Printf("  %-24s %-13s %-13s %-9s %s\n", s.Org+"/"+s.Slot, want, eff, reuse, state)
		}
	}
	for _, w := range rep.Warnings {
		fmt.Printf("  WARNING: %s\n", w)
	}
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check auth, connectivity, and policy access for all configured orgs (use --org for one)",
		RunE: func(*cobra.Command, []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			if err := requireOrgs(mgr); err != nil {
				return err
			}

			ctx := context.Background()
			orgs := mgr.OrgNames()
			if flagOrg != "" {
				if _, ok := mgr.Config().Org(flagOrg); !ok {
					return fmt.Errorf("org %q not configured", flagOrg)
				}
				orgs = []string{flagOrg}
			}

			for i, org := range orgs {
				if i > 0 {
					fmt.Println()
				}
				fmt.Printf("org:         %s\n", org)
				rs, err := mgr.ListRunners(ctx, org)
				if err != nil {
					fmt.Printf("auth/list:   FAIL: %v\n", err)
					continue
				}
				fmt.Printf("auth/list:   OK (%d runners)\n", len(rs))
				if ret, err := mgr.Retention(ctx, org); err != nil {
					fmt.Printf("retention:   unavailable - App likely lacks the Actions-policy permission (%v)\n", err)
				} else {
					fmt.Printf("retention:   %d days (max allowed %d)\n", ret.Days, ret.MaxAllowedDays)
				}
				if res := mgr.Config().ResourcesFor(org); res.IsZero() {
					fmt.Printf("limits:      none - jobs are unbounded; set `resourceMode: auto` to OOM-proof (see config.example.yaml)\n")
				} else if mgr.Config().ResourceMode == config.ResourceModeAuto {
					fmt.Printf("limits:      auto (machine-relative, scales with host RAM) - %s\n", limitsLine(res))
					fmt.Printf("slice cap:   %s (srm.slice - aggregate ceiling for ALL runners combined)\n", mgr.Config().SliceMemoryMaxOrDefault())
				} else {
					fmt.Printf("limits:      %s\n", limitsLine(res))
				}
				printCacheEnv(mgr.Config(), org)
				// Agent freshness (best-effort, observational). state.json is root-only,
				// so without root `total` reads 0 and we just report the published version.
				if cur, total, behind, ok := mgr.AgentVersionStatus(ctx, org); ok {
					switch {
					case len(behind) > 0:
						fmt.Printf("agent:       update available -> %s; behind: %s (run `srm runners upgrade`)\n", cur, strings.Join(behind, ", "))
					case total > 0:
						fmt.Printf("agent:       up to date (%s)\n", cur)
					default:
						fmt.Printf("agent:       GitHub publishes %s\n", cur)
					}
				}
				// App repo visibility (best-effort). An App granted only organization
				// permissions sees only PUBLIC repos, silently breaking the "selected
				// repositories" group repo-picker for private repos.
				if acc, aerr := mgr.AppInstallationAccess(ctx, org); aerr == nil {
					if acc.SeesPrivateRepos {
						fmt.Printf("app repos:   private repos visible (selection: %s)\n", acc.RepositorySelection)
					} else {
						fmt.Printf("app repos:   PRIVATE REPOS INVISIBLE - App has no repository permissions; grant Repository Metadata:read [+ set repo access to All] (selection: %s)\n", acc.RepositorySelection)
					}
				}
			}

			// Host toolchain probe - self-hosted runners ship bare, so jobs that
			// expect node/pnpm/etc. fail with 127 if the host wasn't provisioned.
			// (Reflects this host's PATH; runners inherit the same system dirs.)
			fmt.Println()
			fmt.Println("host toolchain:")
			for _, bin := range []string{"node", "pnpm", "npm", "git", "docker", "make"} {
				if p, err := exec.LookPath(bin); err == nil {
					fmt.Printf("  %-8s %s\n", bin, p)
				} else {
					fmt.Printf("  %-8s (not found - `srm provision`)\n", bin)
				}
			}
			if man := mgr.HostManifest(); !man.Empty() {
				if missing, err := mgr.HostDrift(ctx, man); err == nil {
					if len(missing) == 0 {
						fmt.Println("  manifest: satisfied")
					} else {
						fmt.Printf("  manifest: missing %v (run `srm provision`)\n", missing)
					}
				}
			}

			// Rootless-DinD readiness (Linux only; no-op on Windows). When
			// docker.rootlessDinD is on, these prerequisites fail a build mid-job (not at
			// startup) if absent, so surface them here. Reflects THIS host's kernel + PATH.
			dind := mgr.DinDReadiness(orgs)
			printDinDReadiness(dind)
			printDinDPortRanges(mgr.DinDPortRanges(ctx, orgs))
			printDiskUsage(mgr.DiskUsage(ctx, orgs))

			fmt.Println("\nFor deep host/runner drift + health (and repair), run `srm reconcile`.")
			// A missing hard DinD prerequisite is a real misconfiguration, not an
			// advisory: exit non-zero so scripts/CI notice (details printed above).
			if dind.HasBlocker() {
				return fmt.Errorf("rootless-DinD is enabled but %d host prerequisite(s) are missing - run `sudo srm provision --rootless-dind`", len(dind.Blockers()))
			}
			return nil
		},
	}
}

// printDiskUsage renders the runners' filesystem (free space against minFreeDisk)
// and the disk used by each local ephemeral slot tree, flagging trees an
// interrupted create left without a unit. Advisory: it never fails doctor.
func printDiskUsage(rep service.DiskReport) {
	fmt.Println()
	fmt.Printf("disk (runners' filesystem, %s):\n", rep.Root)
	if rep.Err != "" {
		fmt.Printf("  %-18s unreadable: %s\n", "free", rep.Err)
	} else {
		free := fmt.Sprintf("%s of %s", service.HumanBytes(rep.Avail), service.HumanBytes(rep.Total))
		switch {
		case rep.MinFreeErr != "":
			fmt.Printf("  %-18s %s; INVALID minFreeDisk, ignored by the slots: %s\n", "free", free, rep.MinFreeErr)
		case rep.MinFree == 0:
			fmt.Printf("  %-18s %s; minFreeDisk not set (e.g. `minFreeDisk: 10%%` makes slots wait instead of filling the disk)\n", "free", free)
		case rep.Below():
			fmt.Printf("  %-18s %s; BELOW minFreeDisk %s (%s): ephemeral slots wait before registering for jobs\n",
				"free", free, rep.MinFreeSpec, service.HumanBytes(rep.MinFree))
		default:
			fmt.Printf("  %-18s %s; minFreeDisk %s (%s)\n", "free", free, rep.MinFreeSpec, service.HumanBytes(rep.MinFree))
		}
	}
	if len(rep.Slots) == 0 {
		return
	}
	fmt.Printf("  %-28s %s\n", "slot tree", "size")
	for _, s := range rep.Slots {
		size := service.HumanBytes(s.Bytes)
		switch {
		case s.Partial && s.Bytes == 0:
			size = "not measured (doctor time limit)"
		case s.Partial:
			size = ">= " + size + " (partial)"
		}
		line := fmt.Sprintf("  %-28s %s", s.Org+"/slot "+s.Slot, size)
		if s.Orphan {
			line += fmt.Sprintf("  LEFTOVER: no unit (an interrupted create); the next create of this slot reuses it, or `srm runners destroy --ephemeral --slot %s --org %s` removes it", s.Slot, s.Org)
		}
		fmt.Println(line)
	}
}
