package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/erlete/srm/internal/config"
)

func TestRenderDinDPortWrapper(t *testing.T) {
	w := renderDinDPortWrapper()
	for _, want := range []string{
		"#!/bin/sh\n",
		`if [ "$1" = "--srm-check" ]; then`,
		`netns="$ROOTLESSKIT_STATE_DIR/netns"`,
		`if [ -n "$SRM_DIND_PORT_RANGE" ]; then`,
		`nsenter -n"$netns" sysctl -q -w net.ipv4.ip_local_port_range="$SRM_DIND_PORT_RANGE"`,
		`nsenter -n"$netns" sysctl -q -w net.ipv4.tcp_tw_reuse=1`,
		"dockerd keeps the default range",
		"could not enable tcp_tw_reuse",
		"exec dockerd \"$@\"\n",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("wrapper missing %q:\n%s", want, w)
		}
	}
	// The wrapper must never abort before exec'ing dockerd (fail-open): the only exit
	// is the explicit check mode.
	if strings.Contains(w, "set -e") || strings.Count(w, "exit") != 1 {
		t.Errorf("wrapper must not abort before exec dockerd:\n%s", w)
	}
}

// stubWrapper swaps the install/probe steps for the test and restores them after.
func stubWrapper(t *testing.T, install func(string) error, probe func(context.Context, *user.User, string) error) {
	t.Helper()
	oi, op := installDinDPortWrapper, probeDinDPortWrapper
	installDinDPortWrapper, probeDinDPortWrapper = install, probe
	t.Cleanup(func() { installDinDPortWrapper, probeDinDPortWrapper = oi, op })
}

var testUser = &user.User{Uid: "1001", Gid: "1001", Username: "srm-acme"}

// Partitioning off leaves the daemon env untouched and installs nothing.
func TestDinDPortEnvOff(t *testing.T) {
	stubWrapper(t,
		func(string) error { t.Fatal("install called with partitioning off"); return nil },
		func(context.Context, *user.User, string) error {
			t.Fatal("probe called with partitioning off")
			return nil
		})
	u := &ubuntu{opts: Options{DinD: true}}
	env, note, err := u.dinDPortEnv(context.Background(), "acme", "1", testUser)
	if env != nil || note != "" || err != nil {
		t.Fatalf("off: got %v, %q, %v; want nothing", env, note, err)
	}
}

func TestDinDPortEnvOn(t *testing.T) {
	var installed, probed string
	stubWrapper(t,
		func(p string) error { installed = p; return nil },
		func(_ context.Context, usr *user.User, p string) error { probed = usr.Username + "@" + p; return nil })
	u := &ubuntu{opts: Options{DinD: true, DinDPorts: config.OrgDinD{PortRange: "10000-29999", PortsPerSlot: 500}}}
	env, note, err := u.dinDPortEnv(context.Background(), "acme", "2", testUser)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"DOCKERD=" + DinDPortWrapperPath, "SRM_DIND_PORT_RANGE=10500 10999"}
	if strings.Join(env, "|") != strings.Join(want, "|") {
		t.Fatalf("env = %v, want %v", env, want)
	}
	if installed != DinDPortWrapperPath || probed != "srm-acme@"+DinDPortWrapperPath {
		t.Fatalf("install %q / probe %q not run against the wrapper as the org user", installed, probed)
	}
	if !strings.Contains(note, "acme/2") || !strings.Contains(note, "10500-10999") {
		t.Fatalf("journal note lacks slot/range: %q", note)
	}
}

// The v2.6.0 incident: the wrapper could not be executed (noexec mount). The pre-flight
// must catch it so DOCKERD is never passed and the daemon starts on the default range.
func TestDinDPortEnvNotExecutable(t *testing.T) {
	stubWrapper(t,
		func(string) error { return nil },
		func(context.Context, *user.User, string) error {
			return errors.New("/usr/local/lib/srm is on a noexec mount")
		})
	u := &ubuntu{opts: Options{DinD: true, DinDPorts: config.OrgDinD{PortRange: "10000-29999", PortsPerSlot: 500}}}
	env, _, err := u.dinDPortEnv(context.Background(), "acme", "1", testUser)
	if env != nil {
		t.Fatalf("DOCKERD passed although the wrapper cannot run: %v", env)
	}
	if err == nil || !strings.Contains(err.Error(), "noexec") || !strings.Contains(err.Error(), "srm-acme") {
		t.Fatalf("error should name the user and the noexec cause, got %v", err)
	}
}

func TestDinDPortEnvInstallFails(t *testing.T) {
	stubWrapper(t,
		func(string) error { return errors.New("refusing /usr/local/lib/srm: group-writable") },
		func(context.Context, *user.User, string) error { t.Fatal("probe after a failed install"); return nil })
	u := &ubuntu{opts: Options{DinD: true, DinDPorts: config.OrgDinD{PortRange: "10000-29999"}}}
	if env, _, err := u.dinDPortEnv(context.Background(), "acme", "1", testUser); env != nil || err == nil {
		t.Fatalf("install failure: got %v, %v; want no env and an error", env, err)
	}
}

// A slot past the end of the pool is an error the caller logs (fail-open), never a
// silently wrong range, and nothing is installed for it.
func TestDinDPortEnvSlotPastPool(t *testing.T) {
	stubWrapper(t,
		func(string) error { t.Fatal("install for a slot that does not fit"); return nil },
		func(context.Context, *user.User, string) error { return nil })
	u := &ubuntu{opts: Options{DinD: true, DinDPorts: config.OrgDinD{PortRange: "10000-10999", PortsPerSlot: 500}}}
	if env, _, err := u.dinDPortEnv(context.Background(), "acme", "3", testUser); err == nil || env != nil {
		t.Fatalf("slot 3 of a 2-slot pool: got env %v, err %v; want an error and no env", env, err)
	}
}

func TestPortWrapperPathOverride(t *testing.T) {
	if p := (&ubuntu{}).portWrapperPath(); p != DinDPortWrapperPath {
		t.Fatalf("default path = %q", p)
	}
	if p := (&ubuntu{dinDWrapperPath: "/tmp/w"}).portWrapperPath(); p != "/tmp/w" {
		t.Fatalf("override ignored: %q", p)
	}
	if strings.HasPrefix(DinDPortWrapperPath, "/run/") {
		t.Fatal("the wrapper must not live under /run (noexec on Ubuntu)")
	}
}

func TestParseDinDNetns(t *testing.T) {
	got, err := parseDinDNetns("10500\t10999\n1\n")
	if err != nil || got != (DinDNetns{PortLo: 10500, PortHi: 10999, TWReuse: 1}) {
		t.Fatalf("got %+v %v", got, err)
	}
	for _, bad := range []string{"", "10500 10999", "a b c", "1 2 3 4", "10500\t10999\nx\n"} {
		if _, err := parseDinDNetns(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	// The read command and the parser agree on order: range first, then reuse.
	if len(dinDNetnsReadArgs) != 3 || !strings.HasSuffix(dinDNetnsReadArgs[1], "ip_local_port_range") || !strings.HasSuffix(dinDNetnsReadArgs[2], "tcp_tw_reuse") {
		t.Fatalf("read args out of sync with parseDinDNetns: %v", dinDNetnsReadArgs)
	}
}

func TestDinDStateDir(t *testing.T) {
	if got := filepath.ToSlash(dinDStateDir("acme", "7")); got != "/run/srm/dind/acme/7/dockerd-rootless" {
		t.Fatalf("state dir = %q", got)
	}
}

// The netns is reached through the RootlessKit child's root (the host-side
// <state>/netns is only a placeholder); no child_pid means no running daemon.
func TestDinDNetnsViaChild(t *testing.T) {
	state := t.TempDir()
	if _, err := dinDNetnsViaChild(state); err != ErrDinDNotRunning {
		t.Fatalf("no child_pid: err %v, want ErrDinDNotRunning", err)
	}
	if err := os.WriteFile(filepath.Join(state, "child_pid"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := dinDNetnsViaChild(state); err != ErrDinDNotRunning {
		t.Fatalf("malformed child_pid: err %v, want ErrDinDNotRunning", err)
	}
	if err := os.WriteFile(filepath.Join(state, "child_pid"), []byte("4116\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := dinDNetnsViaChild(state)
	if err != nil {
		t.Fatal(err)
	}
	if want := "/proc/4116/root" + filepath.ToSlash(state) + "/netns"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

// Runs the real wrapper against stub nsenter/dockerd binaries: with a block in the
// env it must target the detached netns when RootlessKit exposes one, write the block
// and tcp_tw_reuse, and exec dockerd with the original arguments; when a write fails it
// must still exec dockerd (fail-open); the check mode must exit 0 without touching
// anything; and without a block it must only exec dockerd.
func TestDinDPortWrapperExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell behavior; rootless DinD is Linux-only")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	type opts struct {
		nsenterExit int
		detached    bool
		block       string
		args        []string
	}
	run := func(t *testing.T, o opts) (calls, stderr string) {
		bin := t.TempDir()
		log := filepath.Join(t.TempDir(), "calls")
		stub := func(name, body string) {
			if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		stub("nsenter", `echo "nsenter $*" >> "$CALLS"; exit `+strconv.Itoa(o.nsenterExit)+"\n")
		stub("dockerd", `echo "dockerd $*" >> "$CALLS"`+"\n")
		state := t.TempDir()
		if o.detached {
			if err := os.WriteFile(filepath.Join(state, "netns"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		wrapper := filepath.Join(t.TempDir(), "dockerd-port-wrapper")
		if err := os.WriteFile(wrapper, []byte(renderDinDPortWrapper()), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(sh, append([]string{wrapper}, o.args...)...)
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "CALLS=" + log, "ROOTLESSKIT_STATE_DIR=" + state}
		if o.block != "" {
			cmd.Env = append(cmd.Env, "SRM_DIND_PORT_RANGE="+o.block)
		}
		var errb strings.Builder
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			t.Fatalf("wrapper failed: %v (stderr %q)", err, errb.String())
		}
		b, _ := os.ReadFile(log)
		return strings.ReplaceAll(string(b), state, "$STATE"), errb.String()
	}
	daemonArgs := []string{"--config-file", "/x/daemon.json", "--data-root", "/x/data"}

	t.Run("detached netns", func(t *testing.T) {
		calls, stderr := run(t, opts{detached: true, block: "10500 10999", args: daemonArgs})
		want := "nsenter -n$STATE/netns sysctl -q -w net.ipv4.ip_local_port_range=10500 10999\n" +
			"nsenter -n$STATE/netns sysctl -q -w net.ipv4.tcp_tw_reuse=1\n" +
			"dockerd --config-file /x/daemon.json --data-root /x/data\n"
		if calls != want {
			t.Fatalf("calls:\n%s\nwant:\n%s", calls, want)
		}
		if stderr != "" {
			t.Fatalf("unexpected stderr %q", stderr)
		}
	})
	t.Run("own netns without detach", func(t *testing.T) {
		calls, _ := run(t, opts{block: "10500 10999", args: daemonArgs})
		if !strings.HasPrefix(calls, "nsenter -n/proc/self/ns/net sysctl") {
			t.Fatalf("without a detached netns the wrapper must target its own: %q", calls)
		}
	})
	t.Run("write fails open", func(t *testing.T) {
		calls, stderr := run(t, opts{nsenterExit: 1, detached: true, block: "10500 10999", args: daemonArgs})
		if !strings.Contains(calls, "dockerd --config-file /x/daemon.json --data-root /x/data") {
			t.Fatalf("dockerd must still start when the sysctl write fails: %q", calls)
		}
		if !strings.Contains(stderr, "could not pin the published-port range 10500 10999") || !strings.Contains(stderr, "could not enable tcp_tw_reuse") {
			t.Fatalf("missing fail-open warnings: %q", stderr)
		}
	})
	t.Run("check mode", func(t *testing.T) {
		if calls, _ := run(t, opts{detached: true, block: "10500 10999", args: []string{dinDWrapperCheckArg}}); calls != "" {
			t.Fatalf("check mode must not touch the netns or exec dockerd: %q", calls)
		}
	})
	t.Run("no block", func(t *testing.T) {
		if calls, _ := run(t, opts{detached: true, args: daemonArgs}); calls != "dockerd --config-file /x/daemon.json --data-root /x/data\n" {
			t.Fatalf("without a block only dockerd may run: %q", calls)
		}
	})
}
