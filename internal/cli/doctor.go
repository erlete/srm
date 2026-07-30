package cli

import (
	"context"
	"fmt"
	"os/exec"
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
