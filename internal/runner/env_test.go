package runner

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/erlete/srm/internal/config"
)

// TestNewWindowsCacheRootDefault pins the fix that NewWindows defaults an empty
// CacheRoot (so single-user mode resolves to %ProgramData%\srm\cache instead of "",
// which would yield root-anchored \npm paths) while leaving an explicit one untouched.
func TestNewWindowsCacheRootDefault(t *testing.T) {
	w := NewWindows(`C:\actions-runners`, Options{}).(*windows)
	if w.opts.CacheRoot != config.DefaultCacheRoot {
		t.Errorf("default CacheRoot = %q, want %q", w.opts.CacheRoot, config.DefaultCacheRoot)
	}
	if w.opts.ToolCache != config.DefaultToolCacheRoot {
		t.Errorf("default ToolCache = %q, want %q", w.opts.ToolCache, config.DefaultToolCacheRoot)
	}
	w2 := NewWindows(`C:\actions-runners`, Options{CacheRoot: `D:\cache`}).(*windows)
	if w2.opts.CacheRoot != `D:\cache` {
		t.Errorf("explicit CacheRoot overwritten: %q", w2.opts.CacheRoot)
	}
}

// TestWinRunnerEnv verifies the injected runner environment: AGENT_TOOLSDIRECTORY first
// (so the list is deterministic), every build-tool cache var present with the same keys
// as config.ToolCacheEnv, separator-normalized values, and stable ordering.
func TestWinRunnerEnv(t *testing.T) {
	opts := Options{ToolCache: `C:\hostedtoolcache`, CacheRoot: `C:\ProgramData\srm\cache`}
	env := winRunnerEnv(opts)

	if len(env) == 0 || env[0] != "AGENT_TOOLSDIRECTORY="+filepath.FromSlash(opts.ToolCache) {
		t.Fatalf("env[0] must be AGENT_TOOLSDIRECTORY=%s, got %v", filepath.FromSlash(opts.ToolCache), env)
	}

	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	for _, e := range config.ToolCacheEnv(opts.CacheRoot) {
		got, ok := m[e.Key]
		if !ok {
			t.Errorf("missing env key %s", e.Key)
			continue
		}
		if got != filepath.FromSlash(e.Val) {
			t.Errorf("%s = %q, want %q", e.Key, got, filepath.FromSlash(e.Val))
		}
	}

	// On Windows the values must be fully backslashed (no stray POSIX separators that a
	// drift check or MSYS tool would misread); GOMODCACHE is the multi-segment canary.
	if runtime.GOOS == "windows" && strings.Contains(m["GOMODCACHE"], "/") {
		t.Errorf("GOMODCACHE has forward slashes on Windows: %q", m["GOMODCACHE"])
	}

	if strings.Join(env, "\n") != strings.Join(winRunnerEnv(opts), "\n") {
		t.Error("winRunnerEnv is not deterministic")
	}
}

// TestWinEphemeralJobEnv verifies the per-job clean-slate profile redirect: the profile
// vars are re-pointed under jobHome (case-insensitively dropped from the inherited env
// first, so no stale duplicate survives), non-profile vars (PATH, the injected cache
// vars) are preserved, and the result is deterministic. Built with filepath/VolumeName
// so the expected values match on whichever OS the mirror test runs on.
func TestWinEphemeralJobEnv(t *testing.T) {
	jobHome := filepath.Join(`C:\actions-runners`, "acme", ".ephemeral", "1", "_home")
	base := []string{
		"PATH=C:\\Windows;C:\\Windows\\System32",
		"AGENT_TOOLSDIRECTORY=C:\\hostedtoolcache",
		"USERPROFILE=C:\\Windows\\ServiceProfiles\\NetworkService",
		"APPDATA=C:\\Windows\\ServiceProfiles\\NetworkService\\AppData\\Roaming",
		"temp=C:\\Windows\\Temp", // lowercase: must still be dropped (case-insensitive)
	}
	out := winEphemeralJobEnv(base, jobHome)

	m := map[string]string{}
	count := map[string]int{}
	for _, kv := range out {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
		count[strings.ToUpper(k)]++
	}

	// Non-profile vars survive untouched.
	if m["PATH"] != "C:\\Windows;C:\\Windows\\System32" {
		t.Errorf("PATH not preserved: %q", m["PATH"])
	}
	if m["AGENT_TOOLSDIRECTORY"] != "C:\\hostedtoolcache" {
		t.Errorf("AGENT_TOOLSDIRECTORY not preserved: %q", m["AGENT_TOOLSDIRECTORY"])
	}

	// Profile vars are re-pointed under jobHome.
	roaming := filepath.Join(jobHome, "AppData", "Roaming")
	local := filepath.Join(jobHome, "AppData", "Local")
	temp := filepath.Join(local, "Temp")
	for k, want := range map[string]string{
		"USERPROFILE":  jobHome,
		"APPDATA":      roaming,
		"LOCALAPPDATA": local,
		"TEMP":         temp,
		"TMP":          temp,
		"HOMEDRIVE":    filepath.VolumeName(jobHome),
		"HOMEPATH":     strings.TrimPrefix(jobHome, filepath.VolumeName(jobHome)),
	} {
		if m[k] != want {
			t.Errorf("%s = %q, want %q", k, m[k], want)
		}
		if count[k] != 1 {
			t.Errorf("%s appears %d times, want exactly 1 (no inherited duplicate)", k, count[k])
		}
	}
	// HOMEDRIVE+HOMEPATH must reconstruct jobHome.
	if m["HOMEDRIVE"]+m["HOMEPATH"] != jobHome {
		t.Errorf("HOMEDRIVE+HOMEPATH = %q, want %q", m["HOMEDRIVE"]+m["HOMEPATH"], jobHome)
	}
	// The inherited (stale) profile values must be gone.
	if strings.Contains(m["USERPROFILE"], "NetworkService") {
		t.Errorf("USERPROFILE still points at the inherited profile: %q", m["USERPROFILE"])
	}
	// Deterministic.
	if strings.Join(out, "\n") != strings.Join(winEphemeralJobEnv(base, jobHome), "\n") {
		t.Error("winEphemeralJobEnv is not deterministic")
	}
}
