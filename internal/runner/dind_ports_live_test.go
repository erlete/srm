//go:build linux

package runner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/erlete/srm/internal/config"
)

// TestLiveDinDPortRanges drives the REAL rootless-DinD start path on this host. It
// needs root, an installed rootless Docker (dockerd-rootless.sh + rootlesskit +
// uidmap + slirp4netns) and an unprivileged user with a subuid/subgid range, so it is
// opt-in:
//
//	SRM_LIVE_DIND=1 SRM_LIVE_DIND_USER=tester SRM_LIVE_DIND_ROOT=/srv/srm-live \
//	SRM_LIVE_DIND_NOEXEC_DIR=/mnt/noexec go test ./internal/runner -run TestLiveDinDPortRanges -v
//
// It proves, for one org:
//   - slots 1 and 2 (wrapper at DinDPortWrapperPath) publish dynamic ports only inside
//     their own dind.portRange block, and ReadDinDNetns reads the block and
//     tcp_tw_reuse=1 back from each slot's network namespace;
//   - slot 3, with the wrapper on a noexec mount (SRM_LIVE_DIND_NOEXEC_DIR, optional),
//     is caught by the pre-flight and still gets a working daemon on the default range;
//   - slot 4, with a wrapper that passes the pre-flight but fails when the rootless
//     script execs it, still gets a working daemon on the default range in the same
//     cycle (the in-cycle fallback).
//
// Run it with /run mounted noexec, as on Ubuntu hosts (mount -o remount,noexec /run).
// SRM_LIVE_DIND_ROOT is the install root; inside a container put it on a tmpfs (an
// overlay data-root on an overlay rootfs cannot mount container layers).
func TestLiveDinDPortRanges(t *testing.T) {
	if os.Getenv("SRM_LIVE_DIND") != "1" {
		t.Skip("set SRM_LIVE_DIND=1 to run against a real rootless Docker on this host")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	name := os.Getenv("SRM_LIVE_DIND_USER")
	if name == "" {
		name = "tester"
	}
	usr, err := user.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("SRM_LIVE_DIND_ROOT")
	if root == "" {
		root = "/srv/srm-live"
	}
	const org = "acme"
	opts := Options{
		User: name, DinD: true, CacheRoot: filepath.Join(root, "cache"),
		BuildkitImage: config.DefaultRootlessBuildkitImage,
		DinDPorts:     config.OrgDinD{PortRange: "10000-11999", PortsPerSlot: 500},
	}
	u := &ubuntu{installRoot: root, opts: opts}
	// The slot trees EnsureBase/CreateEphemeral would create, owned by the user.
	for _, d := range []string{root, filepath.Join(root, org), filepath.Join(root, org, ".ephemeral")} {
		if err := os.MkdirAll(d, 0o711); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(d, 0o711)
	}
	// The shared cache root is traversable, like a provisioned /opt/srm-cache.
	if err := os.MkdirAll(opts.CacheRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	start := func(u *ubuntu, slot string) {
		t.Helper()
		// RunJob creates the slot tree and its per-job _home (user-owned) before it
		// starts the daemon; without _home the per-slot DOCKER_CONFIG is unreachable.
		for _, d := range []string{u.ephemeralSlotDir(org, slot), filepath.Join(u.ephemeralSlotDir(org, slot), "_home")} {
			if err := mkdirOwned(ctx, d, 0o700, name); err != nil {
				t.Fatal(err)
			}
		}
		daemon, env, err := u.startRootlessDocker(ctx, org, slot, usr)
		if err != nil {
			t.Fatalf("slot %s: start rootless dockerd: %v", slot, err)
		}
		t.Cleanup(func() { u.stopRootlessDocker(daemon) })
		t.Logf("slot %s env: %v", slot, env)
	}
	docker := func(slot string, args ...string) string {
		t.Helper()
		rt := u.dindRuntimeDir(org, slot)
		cmd := u.userDockerCmd(ctx, usr, u.userHome(org), u.dindDockerConfigDir(org, slot), rt, filepath.Join(rt, "docker.sock"), append([]string{"docker"}, args...)...)
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("slot %s: docker %v: %v", slot, args, err)
		}
		return strings.TrimSpace(out.String())
	}
	netnsIs := func(slot string, lo, hi, reuse int) {
		t.Helper()
		ns, err := ReadDinDNetns(ctx, org, slot)
		if err != nil || ns.PortLo != lo || ns.PortHi != hi || ns.TWReuse != reuse {
			t.Fatalf("slot %s netns = %+v, %v; want range %d-%d, tcp_tw_reuse %d", slot, ns, err, lo, hi, reuse)
		}
		t.Logf("slot %s netns: %+v", slot, ns)
	}

	// Slots 1 and 2: partitioned through the real wrapper.
	want := map[string][2]int{"1": {10000, 10499}, "2": {10500, 10999}}
	for _, slot := range []string{"1", "2"} {
		start(u, slot)
		netnsIs(slot, want[slot][0], want[slot][1], 1)
		docker(slot, "pull", "-q", "busybox:1.37")
	}
	for range 2 {
		for _, slot := range []string{"1", "2"} {
			id := docker(slot, "run", "-d", "-p", "0:80", "busybox:1.37", "httpd", "-f", "-p", "80")
			hostPort := docker(slot, "port", id, "80/tcp")
			p := hostPort[strings.LastIndex(hostPort, ":")+1:]
			if i := strings.IndexByte(p, '\n'); i >= 0 {
				p = p[:i]
			}
			n, err := strconv.Atoi(p)
			if err != nil {
				t.Fatalf("slot %s: unparsable published port %q", slot, hostPort)
			}
			if n < want[slot][0] || n > want[slot][1] {
				t.Fatalf("slot %s published %d, outside its block %d-%d", slot, n, want[slot][0], want[slot][1])
			}
			t.Logf("slot %s published %d", slot, n)
		}
	}

	// Slot 3: the wrapper sits on a noexec mount. The pre-flight must refuse it, and
	// the slot must still get a working daemon on the kernel default range.
	if dir := os.Getenv("SRM_LIVE_DIND_NOEXEC_DIR"); dir != "" {
		u3 := &ubuntu{installRoot: root, opts: opts, dinDWrapperPath: filepath.Join(dir, "srm", "dockerd-port-wrapper")}
		start(u3, "3")
		netnsIs("3", 32768, 60999, 2)
		docker("3", "info", "--format", "{{.ServerVersion}}")
	} else {
		t.Log("SRM_LIVE_DIND_NOEXEC_DIR unset: skipping the noexec-wrapper slot")
	}

	// Slot 4: a wrapper that passes the pre-flight but fails when the rootless script
	// execs it. The daemon dies at startup; srm must notice at once and relaunch it
	// without the wrapper in the same cycle.
	broken := filepath.Join(root, "broken-wrapper", "dockerd-port-wrapper")
	oi := installDinDPortWrapper
	installDinDPortWrapper = func(path string) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("#!/bin/sh\n[ \"$1\" = \""+dinDWrapperCheckArg+"\" ] && exit 0\nexit 126\n"), 0o755)
	}
	defer func() { installDinDPortWrapper = oi }()
	u4 := &ubuntu{installRoot: root, opts: opts, dinDWrapperPath: broken}
	began := time.Now()
	start(u4, "4")
	t.Logf("slot 4 came up via the fallback in %v", time.Since(began).Round(time.Second))
	netnsIs("4", 32768, 60999, 2)
	docker("4", "info", "--format", "{{.ServerVersion}}")

	b, err := exec.Command("cat", "/proc/sys/net/ipv4/ip_local_port_range").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("host ip_local_port_range: %s", strings.TrimSpace(string(b)))
}
