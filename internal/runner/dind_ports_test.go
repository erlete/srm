package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/erlete/srm/internal/config"
)

func TestRenderDinDPortWrapper(t *testing.T) {
	w := renderDinDPortWrapper(10500, 10999)
	for _, want := range []string{
		"#!/bin/sh\n",
		`netns="$ROOTLESSKIT_STATE_DIR/netns"`,
		`nsenter -n"$netns" sysctl -q -w net.ipv4.ip_local_port_range="10500 10999"`,
		"published-port range 10500-10999; dockerd keeps the default range",
		"exec dockerd \"$@\"\n",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("wrapper missing %q:\n%s", want, w)
		}
	}
	// The wrapper must never abort before exec'ing dockerd (fail-open).
	if strings.Contains(w, "set -e") || strings.Contains(w, "exit") {
		t.Errorf("wrapper must not abort before exec dockerd:\n%s", w)
	}
}

// Partitioning off leaves the daemon env untouched and writes nothing.
func TestDinDPortEnvOff(t *testing.T) {
	rt := t.TempDir()
	u := &ubuntu{opts: Options{DinD: true}}
	entry, note, err := u.dinDPortEnv(rt, "acme", "1")
	if entry != "" || note != "" || err != nil {
		t.Fatalf("off: got %q, %q, %v; want empty", entry, note, err)
	}
	if _, err := os.Stat(filepath.Join(rt, dindPortWrapperName)); !os.IsNotExist(err) {
		t.Fatalf("off: wrapper must not be written (stat err %v)", err)
	}
}

func TestDinDPortEnvOn(t *testing.T) {
	rt := t.TempDir()
	u := &ubuntu{opts: Options{DinD: true, DinDPorts: config.OrgDinD{PortRange: "10000-29999", PortsPerSlot: 500}}}
	entry, note, err := u.dinDPortEnv(rt, "acme", "2")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(rt, dindPortWrapperName)
	if entry != "DOCKERD="+wrapper {
		t.Fatalf("entry = %q, want DOCKERD=%s", entry, wrapper)
	}
	if !strings.Contains(note, "acme/2") || !strings.Contains(note, "10500-10999") {
		t.Fatalf("journal note lacks slot/range: %q", note)
	}
	b, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `ip_local_port_range="10500 10999"`) {
		t.Fatalf("wrapper pins the wrong range:\n%s", b)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(wrapper)
		if fi.Mode().Perm() != 0o755 {
			t.Fatalf("wrapper mode %v, want 0755 so the per-org user can exec it", fi.Mode().Perm())
		}
	}
}

// A slot past the end of the pool is an error the caller logs (fail-open), never a
// silently wrong range.
func TestDinDPortEnvSlotPastPool(t *testing.T) {
	rt := t.TempDir()
	u := &ubuntu{opts: Options{DinD: true, DinDPorts: config.OrgDinD{PortRange: "10000-10999", PortsPerSlot: 500}}}
	if entry, _, err := u.dinDPortEnv(rt, "acme", "3"); err == nil || entry != "" {
		t.Fatalf("slot 3 of a 2-slot pool: got entry %q, err %v; want an error and no entry", entry, err)
	}
	if _, err := os.Stat(filepath.Join(rt, dindPortWrapperName)); !os.IsNotExist(err) {
		t.Fatal("no wrapper may be written when the slot does not fit")
	}
}

func TestParsePortRangeSysctl(t *testing.T) {
	lo, hi, err := parsePortRangeSysctl("10500\t10999\n")
	if err != nil || lo != 10500 || hi != 10999 {
		t.Fatalf("got %d %d %v", lo, hi, err)
	}
	for _, bad := range []string{"", "10500", "a b", "1 2 3"} {
		if _, _, err := parsePortRangeSysctl(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
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

// Runs the real wrapper against stub nsenter/sysctl/dockerd binaries: it must target
// the detached netns when RootlessKit exposes one, write the slot's range, and exec
// dockerd with the original arguments; and when the write fails it must still exec
// dockerd (fail-open) after printing the warning.
func TestDinDPortWrapperExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell behavior; rootless DinD is Linux-only")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	run := func(t *testing.T, nsenterExit int, detached bool) (calls, stderr string) {
		bin := t.TempDir()
		log := filepath.Join(t.TempDir(), "calls")
		stub := func(name, body string) {
			if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		stub("nsenter", `echo "nsenter $*" >> "$CALLS"; exit `+strconv.Itoa(nsenterExit)+"\n")
		stub("dockerd", `echo "dockerd $*" >> "$CALLS"`+"\n")
		state := t.TempDir()
		if detached {
			if err := os.WriteFile(filepath.Join(state, "netns"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		wrapper := filepath.Join(t.TempDir(), dindPortWrapperName)
		if err := os.WriteFile(wrapper, []byte(renderDinDPortWrapper(10500, 10999)), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(sh, wrapper, "--config-file", "/x/daemon.json", "--data-root", "/x/data")
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "CALLS=" + log, "ROOTLESSKIT_STATE_DIR=" + state}
		var errb strings.Builder
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			t.Fatalf("wrapper failed: %v (stderr %q)", err, errb.String())
		}
		b, _ := os.ReadFile(log)
		return strings.ReplaceAll(string(b), state, "$STATE"), errb.String()
	}

	t.Run("detached netns", func(t *testing.T) {
		calls, stderr := run(t, 0, true)
		want := "nsenter -n$STATE/netns sysctl -q -w net.ipv4.ip_local_port_range=10500 10999\n" +
			"dockerd --config-file /x/daemon.json --data-root /x/data\n"
		if calls != want {
			t.Fatalf("calls:\n%s\nwant:\n%s", calls, want)
		}
		if stderr != "" {
			t.Fatalf("unexpected stderr %q", stderr)
		}
	})
	t.Run("own netns without detach", func(t *testing.T) {
		calls, _ := run(t, 0, false)
		if !strings.HasPrefix(calls, "nsenter -n/proc/self/ns/net sysctl") {
			t.Fatalf("without a detached netns the wrapper must target its own: %q", calls)
		}
	})
	t.Run("write fails open", func(t *testing.T) {
		calls, stderr := run(t, 1, true)
		if !strings.Contains(calls, "dockerd --config-file /x/daemon.json --data-root /x/data") {
			t.Fatalf("dockerd must still start when the sysctl write fails: %q", calls)
		}
		if !strings.Contains(stderr, "could not pin the published-port range 10500-10999") {
			t.Fatalf("missing fail-open warning: %q", stderr)
		}
	})
}
