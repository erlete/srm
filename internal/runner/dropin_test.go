package runner

import (
	"fmt"
	"strings"
	"testing"

	"github.com/erlete/srm/internal/config"
)

// TestTemplateVersionMarkers pins the template-generation markers. renderDropIn and
// renderEphemeralUnit must stamp a marker that matches the version const, so a body
// change that bumps the const but not the comment (or vice versa) fails here, and
// unitVersion must parse it back. The marker is what lets reconcile arbitrate
// generations across a mixed-version fleet without thrashing.
func TestTemplateVersionMarkers(t *testing.T) {
	d := renderDropIn(Options{ToolCache: "/t", CacheRoot: "/c"})
	if want := fmt.Sprintf("# srm-dropin-v%d\n", CurrentDropInVersion); !strings.Contains(d, want) {
		t.Errorf("drop-in missing marker %q:\n%s", want, d)
	}
	if got := unitVersion(d, "dropin"); got != CurrentDropInVersion {
		t.Errorf("unitVersion(dropin) = %d, want %d", got, CurrentDropInVersion)
	}
	if !strings.HasPrefix(d, "[Service]\n") { // marker is a comment AFTER [Service]
		t.Errorf("drop-in must still start with [Service]:\n%s", d)
	}

	e := renderEphemeralUnit(Options{SelfExe: "/s"}, "o", "1", "/d")
	if want := fmt.Sprintf("# srm-ephemeral-v%d\n", CurrentEphemeralVersion); !strings.Contains(e, want) {
		t.Errorf("ephemeral unit missing marker %q:\n%s", want, e)
	}
	if got := unitVersion(e, "ephemeral"); got != CurrentEphemeralVersion {
		t.Errorf("unitVersion(ephemeral) = %d, want %d", got, CurrentEphemeralVersion)
	}
	if got := unitVersion(e, "dropin"); got != 0 {
		t.Errorf("ephemeral marker must not be read as a dropin marker, got %d", got)
	}
}

// TestUnitVersionParsing covers the marker parser's edge cases, including the
// authority-relevant malformed inputs (negative, overflow, leading zero, duplicate)
// that must resolve to a safe value rather than a spurious version.
func TestUnitVersionParsing(t *testing.T) {
	cases := []struct {
		in, kind string
		want     int
	}{
		{"[Service]\n# srm-dropin-v1\nProtectHome=true\n", "dropin", 1},
		{"[Service]\n# srm-dropin-v7\n", "dropin", 7},
		{"[Service]\nProtectHome=true\n", "dropin", 0},          // absent => pre-versioning (v0)
		{"# srm-ephemeral-v2\nNoNewPrivileges=true\n", "ephemeral", 2},
		{"# srm-dropin-vX\n", "dropin", 0},                      // non-numeric => 0
		{"  # srm-dropin-v3  \n", "dropin", 3},                  // tolerant of surrounding space
		{"# srm-dropin-v-1\n", "dropin", 0},                     // negative => invalid => 0
		{"# srm-dropin-v99999999999999999999\n", "dropin", 0},   // overflow => 0
		{"# srm-dropin-v01\n", "dropin", 1},                     // leading zero => 1
		{"# srm-dropin-v2\n# srm-dropin-v5\n", "dropin", 2},     // duplicate => first wins (safe: too-low only forces a forward re-render)
	}
	for _, c := range cases {
		if got := unitVersion(c.in, c.kind); got != c.want {
			t.Errorf("unitVersion(%q, %q) = %d, want %d", c.in, c.kind, got, c.want)
		}
	}
}

// TestStripMarker verifies the marker line is removed for body comparison, so a
// markerless v0 field unit and the current render compare equal once stripped (the
// fix that stops an upgrade from flagging every existing unit as drift).
func TestStripMarker(t *testing.T) {
	withMarker := "[Service]\n# srm-dropin-v1\nProtectHome=true\n"
	without := "[Service]\nProtectHome=true\n"
	if got := stripMarker(withMarker, "dropin"); got != without {
		t.Errorf("stripMarker did not remove the marker line: %q", got)
	}
	if got := stripMarker(without, "dropin"); got != without {
		t.Errorf("stripMarker altered a markerless body: %q", got)
	}
	if stripMarker(withMarker, "dropin") != stripMarker(without, "dropin") {
		t.Error("a v1-marked and a markerless copy of the same body must strip equal")
	}
}

// TestTemplateConformance pins the marker-authority arithmetic reconcile relies on,
// using a FIXED current version (decoupled from the live const so an intermediate
// 'older but present' generation is distinct from absent/v0). bodyMatch is the
// marker-stripped body comparison. Covers the newer (authoritative-skip) path that
// can't be exercised on a live host, and the v0-body-match path that must NOT alarm
// on a fleet upgrade.
func TestTemplateConformance(t *testing.T) {
	const cur = 3
	cases := []struct {
		name              string
		onDisk            int
		bodyMatch         bool
		wantOK, wantNewer bool
	}{
		{"equal + body match => conformant", cur, true, true, false},
		{"equal + body differs => stale", cur, false, false, false},
		{"older present + body differs => stale (forward bump)", cur - 1, false, false, false},
		{"older present + body match => conformant (marker is cosmetic)", cur - 1, true, true, false},
		{"absent v0 + body match => conformant (no false alarm on upgrade)", 0, true, true, false},
		{"absent v0 + body differs => stale", 0, false, false, false},
		{"newer => authoritative-skip", cur + 1, true, false, true},
		{"newer + body differs => still skip", cur + 1, false, false, true},
	}
	for _, c := range cases {
		ok, newer := templateConformance(c.onDisk, cur, c.bodyMatch)
		if ok != c.wantOK || newer != c.wantNewer {
			t.Errorf("%s: templateConformance(%d,%d,%v) = (ok=%v,newer=%v), want (ok=%v,newer=%v)",
				c.name, c.onDisk, cur, c.bodyMatch, ok, newer, c.wantOK, c.wantNewer)
		}
	}
}

// TestRenderDropIn pins the drop-in contract. renderDropIn is the single source
// of truth that writeHardening writes and reconcile will diff against, so its
// output must be deterministic and carry the hardening block plus an Environment=
// line for AGENT_TOOLSDIRECTORY and every build-tool cache var.
func TestRenderDropIn(t *testing.T) {
	opts := Options{User: "srm", ToolCache: "/opt/hostedtoolcache", CacheRoot: "/opt/srm-cache"}
	got := renderDropIn(opts)

	if !strings.HasPrefix(got, "[Service]\n") {
		t.Fatalf("drop-in must start with [Service]:\n%s", got)
	}
	if !strings.Contains(got, "Environment=AGENT_TOOLSDIRECTORY=/opt/hostedtoolcache\n") {
		t.Errorf("missing AGENT_TOOLSDIRECTORY line:\n%s", got)
	}
	for _, e := range config.ToolCacheEnv(opts.CacheRoot) {
		want := "Environment=" + e.Key + "=" + e.Val + "\n"
		if !strings.Contains(got, want) {
			t.Errorf("missing cache env line %q", want)
		}
	}

	// Deterministic: reconcile's byte-equality drift check depends on this.
	if got != renderDropIn(opts) {
		t.Error("renderDropIn is not deterministic")
	}

	// Single-user (default) mode must NOT emit a User= directive - the drop-in is
	// byte-identical to the pre-isolation output.
	if strings.Contains(got, "User=") {
		t.Errorf("default-mode drop-in must not contain User=:\n%s", got)
	}

	// Opt-in: with no Resources set, NO cgroup directives are emitted (backward
	// compatible with pre-limits drop-ins).
	for _, k := range []string{"MemoryHigh", "MemoryMax", "MemorySwapMax", "CPUWeight", "TasksMax"} {
		if strings.Contains(got, k+"=") {
			t.Errorf("unset Resources must not emit %s:\n%s", k, got)
		}
	}

	// No slice configured → no Slice= directive (default placement unchanged).
	if strings.Contains(got, "Slice=") {
		t.Errorf("default-mode drop-in must not contain Slice=:\n%s", got)
	}
}

// TestRenderDropInGolden pins the EXACT bytes of the canonical drop-in. Unlike the
// Contains-based checks, this trips on ANY body change - which is the point: if you
// intentionally change the rendered body, update this golden AND bump
// CurrentDropInVersion (and its "# srm-dropin-vN" marker), so a mixed-version fleet
// recognizes the new generation. The byte-equality drift check depends on this body
// staying deterministic.
func TestRenderDropInGolden(t *testing.T) {
	const golden = `[Service]
# srm-dropin-v1
# Managed by srm. Defense-in-depth hardening that is safe for general CI.
ProtectHome=true
PrivateTmp=true
ProtectControlGroups=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectClock=true
ProtectHostname=true
LockPersonality=true
RestrictRealtime=true
# Stricter, opt-in (break sudo/apt - enable only if your jobs never need them):
#   NoNewPrivileges=true
#   ProtectSystem=strict
#   ReadWritePaths=<runner dir>
#   RestrictSUIDSGID=true
Environment=AGENT_TOOLSDIRECTORY=/tc
Environment=npm_config_cache=/cr/npm
Environment=npm_config_store_dir=/cr/pnpm
Environment=YARN_CACHE_FOLDER=/cr/yarn
Environment=PIP_CACHE_DIR=/cr/pip
Environment=GOMODCACHE=/cr/go/mod
Environment=GOCACHE=/cr/go/build
Environment=CARGO_HOME=/cr/cargo
Environment=GRADLE_USER_HOME=/cr/gradle
`
	if got := renderDropIn(Options{ToolCache: "/tc", CacheRoot: "/cr"}); got != golden {
		t.Errorf("drop-in body changed without updating the golden.\nIf intentional, update this golden AND bump CurrentDropInVersion.\n--- got ---\n%s\n--- want ---\n%s", got, golden)
	}
}

// TestRenderDropInSlice verifies auto-capacity mode places the unit in the
// aggregate slice and that renderSlice emits the host-wide memory ceiling. When
// no slice is set, renderSlice is empty (no slice unit written).
func TestRenderDropInSlice(t *testing.T) {
	opts := Options{
		ToolCache:      "/opt/hostedtoolcache",
		CacheRoot:      "/opt/srm-cache",
		Slice:          AggregateSlice,
		SliceMemoryMax: "75%",
	}
	got := renderDropIn(opts)
	if !strings.Contains(got, "Slice=srm.slice\n") {
		t.Errorf("auto-capacity drop-in must place the unit in the slice:\n%s", got)
	}

	slice := renderSlice(opts)
	if !strings.HasPrefix(slice, "[Unit]\n") {
		t.Errorf("slice unit must start with [Unit]:\n%s", slice)
	}
	for _, want := range []string{"[Slice]\n", "MemoryMax=75%\n", "MemorySwapMax=0\n"} {
		if !strings.Contains(slice, want) {
			t.Errorf("slice unit missing %q:\n%s", want, slice)
		}
	}
	if slice != renderSlice(opts) {
		t.Error("renderSlice is not deterministic")
	}
	if s := renderSlice(Options{}); s != "" {
		t.Errorf("no slice configured must yield empty slice unit, got:\n%s", s)
	}
}

// TestRenderDropInResources verifies that set cgroup limits are emitted as
// [Service] directives, set fields only, in a fixed order.
func TestRenderDropInResources(t *testing.T) {
	opts := Options{
		ToolCache: "/opt/hostedtoolcache",
		CacheRoot: "/opt/srm-cache",
		Resources: config.ResourceLimits{
			MemoryHigh:    "2G",
			MemoryMax:     "3G",
			MemorySwapMax: "0",
			TasksMax:      "4096",
			// CPUWeight deliberately unset - must be omitted.
		},
	}
	got := renderDropIn(opts)

	for _, want := range []string{"MemoryHigh=2G\n", "MemoryMax=3G\n", "MemorySwapMax=0\n", "TasksMax=4096\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing directive %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "CPUWeight=") {
		t.Errorf("unset CPUWeight must be omitted:\n%s", got)
	}
	// Fixed order: MemoryMax before MemorySwapMax before TasksMax.
	if i, j, k := strings.Index(got, "MemoryMax="), strings.Index(got, "MemorySwapMax="), strings.Index(got, "TasksMax="); !(i < j && j < k) {
		t.Errorf("directives out of order: MemoryMax=%d MemorySwapMax=%d TasksMax=%d", i, j, k)
	}
}

// TestRenderDropInIsolated verifies isolated mode pins User= and points the
// runner at the per-org tool cache, so RefreshUnit can migrate a runner's user
// in place.
func TestRenderDropInIsolated(t *testing.T) {
	opts := Options{
		User:      "srm-acme",
		ToolCache: "/opt/hostedtoolcache/acme",
		CacheRoot: "/opt/srm-cache/acme",
		Org:       "acme",
		Isolated:  true,
	}
	got := renderDropIn(opts)
	if !strings.Contains(got, "User=srm-acme\n") {
		t.Errorf("isolated drop-in must pin User=srm-acme:\n%s", got)
	}
	if !strings.Contains(got, "Environment=AGENT_TOOLSDIRECTORY=/opt/hostedtoolcache/acme\n") {
		t.Errorf("isolated drop-in must point at the per-org tool cache:\n%s", got)
	}
	if !strings.Contains(got, "Environment=GOMODCACHE=/opt/srm-cache/acme/go/mod\n") {
		t.Errorf("isolated drop-in must point at the per-org dep cache:\n%s", got)
	}
}

// TestRenderDropInProtectProc verifies the opt-in ProtectProc directive: absent by
// default (so the historical drop-in is byte-identical) and present when enabled.
func TestRenderDropInProtectProc(t *testing.T) {
	base := Options{User: "srm", ToolCache: "/opt/hostedtoolcache", CacheRoot: "/opt/srm-cache"}
	if strings.Contains(renderDropIn(base), "ProtectProc") {
		t.Errorf("default drop-in must NOT contain ProtectProc:\n%s", renderDropIn(base))
	}
	on := base
	on.ProtectProc = true
	if got := renderDropIn(on); !strings.Contains(got, "ProtectProc=invisible\n") {
		t.Errorf("ProtectProc enabled drop-in must contain ProtectProc=invisible:\n%s", got)
	}
}

// TestRenderEphemeralUnit pins the full srm-authored ephemeral slot unit: it
// must start as root (NO User=), call `srm _runner-cycle`, restart always, carry
// the stricter ephemeral hardening, and share the env+cap body with the persistent
// drop-in (writeEnvAndLimits).
func TestRenderEphemeralUnit(t *testing.T) {
	opts := Options{
		User:           "srm-acme",
		ToolCache:      "/opt/hostedtoolcache/acme",
		CacheRoot:      "/opt/srm-cache/acme",
		Org:            "acme",
		Isolated:       true,
		Slice:          AggregateSlice,
		SliceMemoryMax: "75%",
		SelfExe:        "/usr/local/bin/srm",
		Resources:      config.ResourceLimits{MemoryMax: "25%", MemorySwapMax: "0"},
	}
	got := renderEphemeralUnit(opts, "acme", "3", "/opt/actions-runners/acme/.ephemeral/3")

	// Root-launched: NO User= (the cycle drops privileges itself after minting).
	if strings.Contains(got, "User=") {
		t.Errorf("ephemeral unit must NOT pin User=:\n%s", got)
	}
	for _, want := range []string{
		"[Unit]\n",
		"StartLimitIntervalSec=0\n",
		"[Service]\n",
		"ExecStart=/usr/local/bin/srm _runner-cycle --org acme --slot 3\n",
		"Restart=always\n",
		"WorkingDirectory=/opt/actions-runners/acme/.ephemeral/3\n",
		"NoNewPrivileges=true\n",
		"ProtectProc=invisible\n",
		"Slice=srm.slice\n",
		"[Install]\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ephemeral unit missing %q:\n%s", want, got)
		}
	}
	// ProcSubset=pid must NOT be set (would hide /proc/cpuinfo and break nproc).
	if strings.Contains(got, "ProcSubset=") {
		t.Errorf("ephemeral unit must not set ProcSubset (breaks nproc):\n%s", got)
	}
	// Shares the env + cgroup body with the persistent path.
	if !strings.Contains(got, "Environment=AGENT_TOOLSDIRECTORY=/opt/hostedtoolcache/acme\n") {
		t.Errorf("missing shared tool-cache env:\n%s", got)
	}
	if !strings.Contains(got, "MemoryMax=25%\n") {
		t.Errorf("missing shared cgroup cap:\n%s", got)
	}
	// Default SelfExe applies when unset.
	if d := renderEphemeralUnit(Options{}, "o", "1", "/d"); !strings.Contains(d, "ExecStart="+DefaultSelfExe+" _runner-cycle --org o --slot 1\n") {
		t.Errorf("default SelfExe not applied:\n%s", d)
	}
	// Deterministic (reconcile will byte-diff it).
	if got != renderEphemeralUnit(opts, "acme", "3", "/opt/actions-runners/acme/.ephemeral/3") {
		t.Error("renderEphemeralUnit is not deterministic")
	}
}

// TestEphemeralNaming pins the ephemeral slot unit name, the minted JIT runner
// name, and the prefix recognition reconcile relies on to exempt ephemeral
// registrations from orphan classification.
func TestEphemeralNaming(t *testing.T) {
	if got := EphemeralSvcName("Acme", "3"); got != "actions.ephemeral.Acme.3.service" {
		t.Errorf("EphemeralSvcName = %q", got)
	}
	name := EphemeralRunnerName("Acme", "3", "abc123")
	if name != "srm-eph-Acme-3-abc123" {
		t.Errorf("EphemeralRunnerName = %q", name)
	}
	if !IsEphemeralRunnerName(name) {
		t.Errorf("IsEphemeralRunnerName(%q) should be true", name)
	}
	if IsEphemeralRunnerName("temporal-1") {
		t.Error("IsEphemeralRunnerName must not match a persistent runner name")
	}

	// EphemeralSlotFromName drives the reaper's safety gate, so its parsing must be
	// exact - including org names with dashes, and rejecting foreign/malformed names.
	for _, c := range []struct {
		name, org, wantSlot string
		wantOK              bool
	}{
		{"srm-eph-Acme-3-abc123", "Acme", "3", true},
		{"srm-eph-Acme-12-deadbeef", "Acme", "12", true},
		{"srm-eph-org2-1-ff", "org2", "1", true},
		{"temporal-1", "Acme", "", false},         // not ephemeral
		{"srm-eph-Other-1-ff", "Acme", "", false}, // wrong org
		{"srm-eph-Acme-3", "Acme", "", false}, // no token segment
	} {
		got, ok := EphemeralSlotFromName(c.name, c.org)
		if ok != c.wantOK || got != c.wantSlot {
			t.Errorf("EphemeralSlotFromName(%q,%q) = (%q,%v), want (%q,%v)", c.name, c.org, got, ok, c.wantSlot, c.wantOK)
		}
	}
}

// TestNewUbuntuDefaults verifies that zero-valued Options fall back to the
// package conventions, so callers override only what differs for an org.
func TestNewUbuntuDefaults(t *testing.T) {
	u, ok := NewUbuntu("/opt/actions-runners", Options{}).(*ubuntu)
	if !ok {
		t.Fatal("NewUbuntu did not return *ubuntu")
	}
	if u.opts.User != config.DefaultRunnerUser {
		t.Errorf("User = %q, want default %q", u.opts.User, config.DefaultRunnerUser)
	}
	if u.opts.ToolCache != config.DefaultToolCacheRoot {
		t.Errorf("ToolCache = %q, want default %q", u.opts.ToolCache, config.DefaultToolCacheRoot)
	}
	if u.opts.CacheRoot != config.DefaultCacheRoot {
		t.Errorf("CacheRoot = %q, want default %q", u.opts.CacheRoot, config.DefaultCacheRoot)
	}

	// Explicit values win over defaults.
	u2 := NewUbuntu("/opt/actions-runners", Options{User: "srm-acme", CacheRoot: "/opt/srm-cache/acme"}).(*ubuntu)
	if u2.opts.User != "srm-acme" || u2.opts.CacheRoot != "/opt/srm-cache/acme" {
		t.Errorf("explicit opts not preserved: %+v", u2.opts)
	}
	if u2.opts.ToolCache != config.DefaultToolCacheRoot {
		t.Errorf("unset ToolCache should still default, got %q", u2.opts.ToolCache)
	}
}
