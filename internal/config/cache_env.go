package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// OrgCache holds an org's build-tool cache settings.
type OrgCache struct {
	// Env adjusts the cache env vars srm injects into this org's runners, as
	// NAME: subdirectory of the org's cache root. An entry adds a variable or
	// re-points a default; an empty value removes a default. See ToolCacheEnv.
	Env map[string]string `koanf:"env" yaml:"env,omitempty"`
}

// IsZero reports whether no per-org cache setting is configured (yaml omitempty).
func (c OrgCache) IsZero() bool { return len(c.Env) == 0 }

// defaultToolCacheEnv is the built-in cache env, as NAME -> subdirectory of the
// cache root. Only caches that are safe under concurrent jobs belong here: they
// are shared by every runner of the org, and parallel ephemeral slots write them
// at the same time. Mutable install trees and build-output caches (mise's data
// dir, Turborepo) stay opt-in through orgs[].cache.env. The order is the drop-in
// order, so it must stay stable for reconcile's byte-equality drift check.
var defaultToolCacheEnv = []struct{ key, sub string }{
	{"npm_config_cache", "npm"},                // npm
	{"npm_config_store_dir", "pnpm"},           // pnpm <= 10 content-addressable store
	{"pnpm_config_store_dir", "pnpm"},          // pnpm >= 11 (no longer reads npm_config_*); same store, versioned subdirs
	{"YARN_CACHE_FOLDER", "yarn"},              // yarn (classic)
	{"COREPACK_HOME", "corepack"},              // corepack's downloaded package managers
	{"PIP_CACHE_DIR", "pip"},                   // pip
	{"UV_CACHE_DIR", "uv"},                     // uv
	{"GOMODCACHE", "go/mod"},                   // go module cache
	{"GOCACHE", "go/build"},                    // go build cache
	{"CARGO_HOME", "cargo"},                    // cargo registry + git caches
	{"GRADLE_USER_HOME", "gradle"},             // gradle
	{"PLAYWRIGHT_BROWSERS_PATH", "playwright"}, // Playwright browsers (version-keyed dirs, install lock)
	{"MISE_CACHE_DIR", "mise/cache"},           // mise download cache
}

// isDefaultCacheKey reports whether key is one of the built-in cache env vars.
func isDefaultCacheKey(key string) bool {
	for _, d := range defaultToolCacheEnv {
		if d.key == key {
			return true
		}
	}
	return false
}

// ToolCacheEnv returns the build-tool cache-location env vars pointing each
// package manager at a host-persistent directory under root: the built-in
// defaults, adjusted by an org's cache.env overrides. Injected into every
// runner's unit (process env) so the tools, which run in `run:` steps and read
// these from the environment, keep their caches on the host rather than in the
// job's wiped HOME or GitHub's capped cache.
//
// Defaults come first in their fixed order (a re-pointed default keeps its
// place, a removed one is skipped), then added variables sorted by name, so the
// generated unit is deterministic. Invalid override entries are dropped, never
// rendered (see CacheEnvProblems, which `srm doctor` reports): a bad entry costs
// only that cache, never the unit or the lane. Paths are POSIX; Windows callers
// convert them.
func ToolCacheEnv(root string, overrides map[string]string) []EnvVar {
	ok := validCacheEnv(overrides)
	out := make([]EnvVar, 0, len(defaultToolCacheEnv)+len(ok))
	for _, d := range defaultToolCacheEnv {
		sub := d.sub
		if v, set := ok[d.key]; set {
			if v == "" {
				continue
			}
			sub = v
		}
		out = append(out, EnvVar{d.key, root + "/" + sub})
	}
	extra := make([]string, 0, len(ok))
	for k, v := range ok {
		if !isDefaultCacheKey(k) && v != "" {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		out = append(out, EnvVar{k, root + "/" + ok[k]})
	}
	return out
}

// CacheEnvProblems lists the cache.env entries ToolCacheEnv drops, one message
// per entry, sorted. Empty means every entry applies.
func CacheEnvProblems(overrides map[string]string) []string {
	var out []string
	for _, k := range sortedKeys(overrides) {
		if err := cacheEnvEntryError(k, overrides[k], overrides); err != nil {
			out = append(out, err.Error())
		}
	}
	return out
}

// validCacheEnv returns the override entries that pass validation.
func validCacheEnv(overrides map[string]string) map[string]string {
	ok := make(map[string]string, len(overrides))
	for k, v := range overrides {
		if cacheEnvEntryError(k, v, overrides) == nil {
			ok[k] = v
		}
	}
	return ok
}

var (
	cacheEnvKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// A subdirectory is one or more plain segments joined by "/". The narrow
	// charset also keeps the value safe on a systemd Environment= line (no
	// whitespace, quotes, "%" specifiers, "$" or "\").
	cacheEnvSubRe = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
)

// reservedCacheEnvKeys are variables srm or the runner sets, or that load code
// (a shared, job-writable directory there would let one job inject code into
// the next). Compared case-insensitively, since Windows env names are.
var reservedCacheEnvKeys = map[string]bool{
	"HOME": true, "PATH": true, "USER": true, "LOGNAME": true, "SHELL": true, "PWD": true,
	"TMPDIR": true, "TEMP": true, "TMP": true, "XDG_RUNTIME_DIR": true,
	"USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true, "HOMEDRIVE": true, "HOMEPATH": true,
	"AGENT_TOOLSDIRECTORY": true,
	"PYTHONPATH":           true, "NODE_PATH": true, "NODE_OPTIONS": true, "PERL5LIB": true, "RUBYLIB": true, "CLASSPATH": true,
}

var reservedCacheEnvPrefixes = []string{"RUNNER_", "GITHUB_", "ACTIONS_", "SRM_", "DOCKER_", "LD_", "DYLD_"}

// cacheEnvEntryError validates one cache.env entry; all is the whole map, for
// the case-collision check.
func cacheEnvEntryError(key, val string, all map[string]string) error {
	if !cacheEnvKeyRe.MatchString(key) {
		return fmt.Errorf("%q: not a valid environment variable name", key)
	}
	up := strings.ToUpper(key)
	if reservedCacheEnvKeys[up] {
		return fmt.Errorf("%s: reserved (srm or the runner sets it, or it loads code)", key)
	}
	for _, p := range reservedCacheEnvPrefixes {
		if strings.HasPrefix(up, p) {
			return fmt.Errorf("%s: reserved prefix %s", key, p)
		}
	}
	// Names that differ only in case are one variable on Windows; refuse the
	// ambiguity on every OS so a config behaves the same on both.
	for _, d := range defaultToolCacheEnv {
		if d.key != key && strings.EqualFold(d.key, key) {
			return fmt.Errorf("%s: differs only in case from the default %s", key, d.key)
		}
	}
	for other := range all {
		if other != key && strings.EqualFold(other, key) {
			return fmt.Errorf("%s: differs only in case from %s", key, other)
		}
	}
	if val == "" {
		if !isDefaultCacheKey(key) {
			return fmt.Errorf("%s: an empty value removes a default, and %s is not one", key, key)
		}
		return nil
	}
	if !cacheEnvSubRe.MatchString(val) {
		return fmt.Errorf("%s: %q must be a relative subdirectory of the cache root (letters, digits, '.', '_', '-' and '/')", key, val)
	}
	for seg := range strings.SplitSeq(val, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("%s: %q must stay inside the cache root (no '.' or '..' segments)", key, val)
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// installTreeCacheKeys are cache vars whose directory holds unpacked install
// trees (browsers, package managers, unpacked wheels, installed tools) rather
// than self-contained cache files. Deleting a single file there leaves an install
// that still looks complete but is broken, so `srm cache prune`, which evicts
// file by file, must skip them; each tool has its own pruning (`uv cache prune`,
// `mise prune`, reinstalling a Playwright browser).
var installTreeCacheKeys = map[string]bool{
	"COREPACK_HOME":            true,
	"UV_CACHE_DIR":             true,
	"PLAYWRIGHT_BROWSERS_PATH": true,
	"MISE_DATA_DIR":            true,
}

// InstallTreeCacheDirs returns the effective directories (see ToolCacheEnv) that
// hold install trees, which `srm cache prune` must not evict file by file.
func InstallTreeCacheDirs(root string, overrides map[string]string) []string {
	var out []string
	for _, e := range ToolCacheEnv(root, overrides) {
		if installTreeCacheKeys[e.Key] {
			out = append(out, e.Val)
		}
	}
	return out
}
