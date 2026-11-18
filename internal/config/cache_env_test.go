package config

import (
	"reflect"
	"strings"
	"testing"
)

func envMap(t *testing.T, env []EnvVar) map[string]string {
	t.Helper()
	m := make(map[string]string, len(env))
	for _, e := range env {
		if _, dup := m[e.Key]; dup {
			t.Fatalf("duplicate key %s in %v", e.Key, env)
		}
		m[e.Key] = e.Val
	}
	return m
}

// pnpm >= 11 reads only pnpm_config_*, so both names must point at the one store.
func TestToolCacheEnvDefaults(t *testing.T) {
	m := envMap(t, ToolCacheEnv("/cr", nil))
	want := map[string]string{
		"npm_config_store_dir":     "/cr/pnpm",
		"pnpm_config_store_dir":    "/cr/pnpm",
		"UV_CACHE_DIR":             "/cr/uv",
		"COREPACK_HOME":            "/cr/corepack",
		"PLAYWRIGHT_BROWSERS_PATH": "/cr/playwright",
		"MISE_CACHE_DIR":           "/cr/mise/cache",
		"GOMODCACHE":               "/cr/go/mod",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	// Opt-in only: mutable install trees and build-output caches.
	for _, k := range []string{"MISE_DATA_DIR", "TURBO_CACHE_DIR"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s must not be a default", k)
		}
	}
	if len(m) != len(defaultToolCacheEnv) {
		t.Errorf("got %d vars, want %d", len(m), len(defaultToolCacheEnv))
	}
}

func TestToolCacheEnvOverrides(t *testing.T) {
	ov := map[string]string{
		"TURBO_CACHE_DIR": "turbo",     // add
		"MISE_DATA_DIR":   "mise/data", // add
		"UV_CACHE_DIR":    "python/uv", // re-point a default
		"GOCACHE":         "",          // remove a default
		"HOME":            "home",      // reserved: dropped
		"BAD":             "../escape", // escapes the root: dropped
		"ALSO_BAD":        "a b",       // whitespace: dropped
		"NOT_DEFAULT":     "",          // empty on a non-default: dropped
	}
	env := ToolCacheEnv("/cr", ov)
	m := envMap(t, env)

	if m["TURBO_CACHE_DIR"] != "/cr/turbo" || m["MISE_DATA_DIR"] != "/cr/mise/data" {
		t.Errorf("added vars not rendered: %v", m)
	}
	if m["UV_CACHE_DIR"] != "/cr/python/uv" {
		t.Errorf("UV_CACHE_DIR = %q, want re-pointed", m["UV_CACHE_DIR"])
	}
	for _, k := range []string{"GOCACHE", "HOME", "BAD", "ALSO_BAD", "NOT_DEFAULT"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s must not be rendered", k)
		}
	}

	// Defaults keep their order (a re-pointed one keeps its place); added vars
	// follow sorted, so the unit is deterministic.
	var keys []string
	for _, e := range env {
		keys = append(keys, e.Key)
	}
	var wantKeys []string
	for _, d := range defaultToolCacheEnv {
		if d.key != "GOCACHE" {
			wantKeys = append(wantKeys, d.key)
		}
	}
	wantKeys = append(wantKeys, "MISE_DATA_DIR", "TURBO_CACHE_DIR")
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Errorf("order:\n got %v\nwant %v", keys, wantKeys)
	}
	for range 20 { // map iteration order must not leak into the output
		if !reflect.DeepEqual(ToolCacheEnv("/cr", ov), env) {
			t.Fatal("ToolCacheEnv is not deterministic")
		}
	}
}

func TestCacheEnvProblems(t *testing.T) {
	if p := CacheEnvProblems(nil); len(p) != 0 {
		t.Fatalf("nil map: %v", p)
	}
	good := map[string]string{"TURBO_CACHE_DIR": "turbo", "GOCACHE": "", "X_1": "a/b-c/d.e_f"}
	if p := CacheEnvProblems(good); len(p) != 0 {
		t.Fatalf("valid entries reported: %v", p)
	}

	bad := map[string]string{
		"1BAD":              "x",                  // invalid name
		"PATH":              "bin",                // reserved
		"path":              "bin",                // reserved, case-insensitive
		"LD_LIBRARY_PATH":   "lib",                // code-loading prefix
		"GITHUB_TOKEN":      "t",                  // runner prefix
		"UV_cache_dir":      "uv",                 // differs only in case from a default
		"Foo":               "a",                  // case collision with FOO
		"FOO":               "b",                  // case collision with Foo
		"ABS":               "/etc",               // absolute
		"DOTDOT":            "a/../b",             // escapes
		"DOT":               ".",                  // the root itself
		"WIN":               `a\b`,                // backslash
		"SPEC":              "a%h",                // systemd specifier
		"NL":                "a\nEnvironment=X=y", // line injection
		"EMPTY_NOT_DEFAULT": "",                   // empty on a non-default
		"TRAILING":          "a/",                 // empty segment
	}
	p := CacheEnvProblems(bad)
	if len(p) != len(bad) {
		t.Fatalf("got %d problems for %d bad entries:\n%s", len(p), len(bad), strings.Join(p, "\n"))
	}
	if got := ToolCacheEnv("/cr", bad); len(got) != len(defaultToolCacheEnv) {
		t.Errorf("bad entries changed the env: %v", got)
	}
}
