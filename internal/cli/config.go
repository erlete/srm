package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
	"github.com/erlete/srm/internal/setup"
)

// newConfigCmd is the CLI mirror of the TUI Settings host-policy toggles: the
// host-wide config that used to be hand-edit-only (rootless DinD, its BuildKit image,
// per-org isolation, ProtectProc hardening). Secrets are never printed.
func newConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "Show or set host-wide policy toggles (rootless DinD, isolation, hardening)",
	}
	c.AddCommand(newConfigShowCmd(), newConfigSetCmd(), newConfigEditOrgCmd(), newConfigManifestCmd())
	return c
}

// newConfigEditOrgCmd re-edits an existing org through the SAME prefilled form as the
// TUI edit flow (slug locked, key defaults to keep-current). It is the CLI mirror of
// the TUI "Edit org" lifecycle action.
func newConfigEditOrgCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit-org",
		Short: "Re-edit an existing org's App credentials + runner defaults (prefilled; --org, or the sole org)",
		RunE: func(*cobra.Command, []string) error {
			cfg, err := config.Load(flagConfig)
			if err != nil {
				return err
			}
			org := flagOrg
			if org == "" {
				switch len(cfg.Orgs) {
				case 0:
					return fmt.Errorf("no orgs configured - run `srm init`")
				case 1:
					org = cfg.Orgs[0].Name
				default:
					return fmt.Errorf("multiple orgs configured - specify --org")
				}
			}
			oc, ok := cfg.Org(org)
			if !ok {
				return fmt.Errorf("org %q not configured", org)
			}
			edited, err := collectOrg(setup.FieldsFromOrgConfig(*oc), true)
			if err != nil {
				return err
			}
			upsertOrg(cfg, edited)
			if err := writeConfig(flagConfig, cfg); err != nil {
				return err
			}
			fmt.Printf("Updated %s (org %q).\n", flagConfig, edited.Name)
			if err := validateOrgAuth(edited.Name); err != nil {
				fmt.Printf("⚠ saved, but GitHub auth check failed: %v\n", err)
				fmt.Println("Re-run `srm config edit-org` to fix, or `srm doctor` to recheck.")
				return nil
			}
			fmt.Printf("✓ GitHub auth verified for %q.\n", edited.Name)
			return nil
		},
	}
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the editable host-wide policy (no secrets)",
		RunE: func(*cobra.Command, []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			p := mgr.HostPolicy()
			bk := p.BuildkitImage
			if bk == "" {
				bk = "(default)"
			}
			mode := mgr.ResourceSettings().Mode
			if mode == "" {
				mode = "(manual/off)"
			}
			fmt.Printf("config file:           %s\n", mgr.ConfigPath())
			fmt.Printf("docker.rootlessDinD:   %t\n", p.RootlessDinD)
			fmt.Printf("docker.buildkitImage:  %s\n", bk)
			fmt.Printf("isolation.perOrgUsers: %t\n", p.PerOrgUsers)
			fmt.Printf("hardening.protectProc: %t\n", p.ProtectProc)
			fmt.Printf("resourceMode:          %s\n", mode)
			return nil
		},
	}
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set key=value [key=value ...]",
		Short: "Set + persist host-wide policy toggles (docker.rootlessDinD, docker.buildkitImage, isolation.perOrgUsers, hardening.protectProc)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			p := mgr.HostPolicy()
			for _, a := range args {
				k, v, ok := strings.Cut(a, "=")
				if !ok {
					return fmt.Errorf("expected key=value, got %q", a)
				}
				k, v = strings.TrimSpace(k), strings.TrimSpace(v)
				switch k {
				case "docker.rootlessDinD":
					b, err := parseConfigBool(v)
					if err != nil {
						return fmt.Errorf("%s: %w", k, err)
					}
					p.RootlessDinD = b
				case "docker.buildkitImage":
					p.BuildkitImage = v
				case "isolation.perOrgUsers":
					b, err := parseConfigBool(v)
					if err != nil {
						return fmt.Errorf("%s: %w", k, err)
					}
					p.PerOrgUsers = b
				case "hardening.protectProc":
					b, err := parseConfigBool(v)
					if err != nil {
						return fmt.Errorf("%s: %w", k, err)
					}
					p.ProtectProc = b
				default:
					return fmt.Errorf("unknown key %q (settable: docker.rootlessDinD, docker.buildkitImage, isolation.perOrgUsers, hardening.protectProc)", k)
				}
			}
			if err := mgr.ApplyHostPolicy(p); err != nil {
				return err
			}
			fmt.Printf("saved host policy → %s\n", mgr.ConfigPath())
			if p.RootlessDinD {
				fmt.Println("note: rootless DinD needs its host prereqs - run `sudo srm provision --rootless-dind`, then `srm doctor` to verify.")
			}
			return nil
		},
	}
}

// parseConfigBool accepts the usual boolean spellings plus on/off/yes/no.
func parseConfigBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "yes", "y":
		return true, nil
	case "off", "no", "n":
		return false, nil
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return false, fmt.Errorf("expected a boolean (true/false/on/off)")
	}
	return b, nil
}

// newConfigManifestCmd is the CLI mirror of the TUI host-manifest editor: the
// data-driven `host:` dependency layer `srm provision` applies (apt packages, setup
// scripts, tool-cache seeds, persistent cache paths). It removes the last hand-edit-
// only config for the toolchain. Setup scripts may be pre-placed paths (--script) or
// inline bodies (--script-file reads a local file's CONTENTS in), materialized at
// provision time (P4).
func newConfigManifestCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "manifest",
		Short: "Show or edit the host dependency manifest (apt packages, setup scripts, tool-cache seeds, cache paths)",
	}
	c.AddCommand(newConfigManifestShowCmd(), newConfigManifestAddCmd(), newConfigManifestRemoveCmd())
	return c
}

func newConfigManifestShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the host dependency manifest",
		RunE: func(*cobra.Command, []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			fmt.Printf("config file: %s\n", mgr.ConfigPath())
			printManifest(mgr.HostManifest())
			return nil
		},
	}
}

func newConfigManifestAddCmd() *cobra.Command {
	var apt, seed, seedNode, cache, script, scriptFile []string
	c := &cobra.Command{
		Use:   "add",
		Short: "Add entries to the host manifest (idempotent; deduped)",
		Long: "Add entries to the host dependency manifest and persist them. --script takes a " +
			"path to a script already on the host; --script-file reads a LOCAL file's contents and " +
			"stores them INLINE, so no file need be pre-placed on the target (materialized at provision).",
		RunE: func(_ *cobra.Command, _ []string) error {
			if len(apt)+len(seed)+len(seedNode)+len(cache)+len(script)+len(scriptFile) == 0 {
				return fmt.Errorf("nothing to add - pass --apt/--seed/--seed-node/--cache/--script/--script-file")
			}
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			m := mgr.HostManifest()
			m.AptPackages = appendUnique(m.AptPackages, trimAll(apt)...)
			m.ToolCacheSeeds = appendUnique(m.ToolCacheSeeds, trimAll(seed)...)
			for _, v := range trimAll(seedNode) {
				m.ToolCacheSeeds = appendUnique(m.ToolCacheSeeds, "node@"+v)
			}
			m.PersistentCachePaths = appendUnique(m.PersistentCachePaths, trimAll(cache)...)
			m.SetupScripts = appendUnique(m.SetupScripts, trimAll(script)...)
			for _, f := range scriptFile {
				body, err := os.ReadFile(strings.TrimSpace(f))
				if err != nil {
					return fmt.Errorf("read script file %s: %w", f, err)
				}
				m.SetupScripts = appendUnique(m.SetupScripts, string(body)) // stored inline, verbatim
			}
			if err := mgr.ApplyHostManifest(m); err != nil {
				return err
			}
			fmt.Printf("saved host manifest → %s\n", mgr.ConfigPath())
			printManifest(m)
			fmt.Println("note: run `sudo srm provision` to apply it to the host.")
			return nil
		},
	}
	c.Flags().StringSliceVar(&apt, "apt", nil, "apt package(s) to add (repeatable / comma-separated)")
	c.Flags().StringSliceVar(&seed, "seed", nil, "tool@version tool-cache seed(s) (e.g. node@22.11.0,go@1.23.4)")
	c.Flags().StringSliceVar(&seedNode, "seed-node", nil, "shorthand for --seed node@<ver>")
	c.Flags().StringSliceVar(&cache, "cache", nil, "persistent cache path(s) kept across jobs")
	c.Flags().StringSliceVar(&script, "script", nil, "setup script PATH(s) already on the host")
	c.Flags().StringSliceVar(&scriptFile, "script-file", nil, "local script file(s) stored INLINE (materialized at provision; no pre-placed file needed)")
	return c
}

func newConfigManifestRemoveCmd() *cobra.Command {
	var apt, seed, cache, script []string
	c := &cobra.Command{
		Use:   "remove",
		Short: "Remove entries from the host manifest (exact match)",
		RunE: func(_ *cobra.Command, _ []string) error {
			if len(apt)+len(seed)+len(cache)+len(script) == 0 {
				return fmt.Errorf("nothing to remove - pass --apt/--seed/--cache/--script")
			}
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			m := mgr.HostManifest()
			m.AptPackages = removeAll(m.AptPackages, trimAll(apt)...)
			m.ToolCacheSeeds = removeAll(m.ToolCacheSeeds, trimAll(seed)...)
			m.PersistentCachePaths = removeAll(m.PersistentCachePaths, trimAll(cache)...)
			m.SetupScripts = removeAll(m.SetupScripts, trimAll(script)...)
			if err := mgr.ApplyHostManifest(m); err != nil {
				return err
			}
			fmt.Printf("saved host manifest → %s\n", mgr.ConfigPath())
			printManifest(m)
			return nil
		},
	}
	c.Flags().StringSliceVar(&apt, "apt", nil, "apt package(s) to remove")
	c.Flags().StringSliceVar(&seed, "seed", nil, "tool-cache seed(s) to remove")
	c.Flags().StringSliceVar(&cache, "cache", nil, "persistent cache path(s) to remove")
	c.Flags().StringSliceVar(&script, "script", nil, "setup script path(s) to remove (inline bodies are TUI-editable)")
	return c
}

// printManifest renders the host manifest without dumping inline script bodies.
func printManifest(m core.DependencyManifest) {
	if m.Empty() {
		fmt.Println("host manifest is empty (bring-your-own-host).")
		return
	}
	show := func(label string, xs []string, scripts bool) {
		if len(xs) == 0 {
			return
		}
		fmt.Printf("%s:\n", label)
		for _, x := range xs {
			if scripts && core.IsInlineScript(x) {
				fmt.Printf("  - (inline script, %d bytes)\n", len(x))
			} else {
				fmt.Printf("  - %s\n", x)
			}
		}
	}
	show("apt packages", m.AptPackages, false)
	show("setup scripts", m.SetupScripts, true)
	show("tool-cache seeds", m.ToolCacheSeeds, false)
	show("persistent cache paths", m.PersistentCachePaths, false)
}

// appendUnique appends the given values to dst, skipping empties and duplicates.
func appendUnique(dst []string, add ...string) []string {
	seen := make(map[string]bool, len(dst))
	for _, x := range dst {
		seen[x] = true
	}
	for _, a := range add {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		dst = append(dst, a)
	}
	return dst
}

// removeAll returns dst without any element equal to a value in rm.
func removeAll(dst []string, rm ...string) []string {
	if len(rm) == 0 {
		return dst
	}
	drop := make(map[string]bool, len(rm))
	for _, r := range rm {
		drop[r] = true
	}
	var out []string
	for _, x := range dst {
		if !drop[x] {
			out = append(out, x)
		}
	}
	return out
}

// trimAll trims surrounding whitespace off each value, dropping any that empty out.
func trimAll(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if t := strings.TrimSpace(x); t != "" {
			out = append(out, t)
		}
	}
	return out
}
