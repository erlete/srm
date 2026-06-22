package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/erlete/srm/internal/config"
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
			dindReadiness(mgr.Config(), orgs)

			fmt.Println("\nFor deep host/runner drift + health (and repair), run `srm reconcile`.")
			return nil
		},
	}
}
