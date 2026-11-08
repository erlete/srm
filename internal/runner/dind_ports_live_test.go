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

// TestLiveDinDPortRanges drives the REAL rootless-DinD start path for two slots of one
// org on this host and proves each daemon publishes dynamic ports only inside its own
// dind.portRange block, and that ReadDinDNetns reads that block (and tcp_tw_reuse=1)
// back from the slot's network namespace. It needs root, an installed rootless Docker
// (dockerd-rootless.sh + rootlesskit + uidmap + slirp4netns) and an unprivileged user
// with a subuid/subgid range, so it is opt-in:
//
//	SRM_LIVE_DIND=1 SRM_LIVE_DIND_USER=tester SRM_LIVE_DIND_ROOT=/srv/srm-live go test ./internal/runner -run TestLiveDinDPortRanges -v
//
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
	u := &ubuntu{installRoot: root, opts: Options{
		User: name, DinD: true, CacheRoot: filepath.Join(root, "cache"),
		BuildkitImage: config.DefaultRootlessBuildkitImage,
		DinDPorts:     config.OrgDinD{PortRange: "10000-10999", PortsPerSlot: 500},
	}}
	// The slot trees EnsureBase/CreateEphemeral would create, owned by the user.
	for _, d := range []string{root, filepath.Join(root, org), filepath.Join(root, org, ".ephemeral")} {
		if err := os.MkdirAll(d, 0o711); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(d, 0o711)
	}
	// The shared cache root is traversable, like a provisioned /opt/srm-cache.
	if err := os.MkdirAll(u.opts.CacheRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	want := map[string][2]int{"1": {10000, 10499}, "2": {10500, 10999}}
	for slot := range want {
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

	for slot, w := range want {
		ns, err := ReadDinDNetns(ctx, org, slot)
		if err != nil || ns.PortLo != w[0] || ns.PortHi != w[1] || ns.TWReuse != 1 {
			t.Fatalf("slot %s netns = %+v, %v; want range %d-%d with tcp_tw_reuse 1", slot, ns, err, w[0], w[1])
		}
	}

	// Publish dynamic ports on both daemons, interleaved: each must stay in its block.
	docker := func(slot string, args ...string) string {
		rt := u.dindRuntimeDir(org, slot)
		cmd := u.userDockerCmd(ctx, usr, u.userHome(org), u.dindDockerConfigDir(org, slot), rt, filepath.Join(rt, "docker.sock"), append([]string{"docker"}, args...)...)
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("slot %s: docker %v: %v", slot, args, err)
		}
		return strings.TrimSpace(out.String())
	}
	for _, slot := range []string{"1", "2"} {
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

	// The host's own range is untouched.
	b, err := exec.Command("cat", "/proc/sys/net/ipv4/ip_local_port_range").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("host ip_local_port_range: %s", strings.TrimSpace(string(b)))
}
