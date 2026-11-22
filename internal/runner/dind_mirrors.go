package runner

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/erlete/srm/internal/config"
)

// rootlessDaemonConfig is the daemon.json srm pins for a slot's rootless dockerd:
// the cgroupfs driver (see startRootlessDocker) and, when docker.registryMirrors is
// set, the mirrors dockerd tries before docker.io for its own pulls (docker pull,
// compose, testcontainers), with the plain-http ones listed as insecure registries.
// dockerd falls back to docker.io when a mirror fails.
func rootlessDaemonConfig(mirrors []config.RegistryMirror) []byte {
	cfg := struct {
		ExecOpts           []string `json:"exec-opts"`
		RegistryMirrors    []string `json:"registry-mirrors,omitempty"`
		InsecureRegistries []string `json:"insecure-registries,omitempty"`
	}{ExecOpts: []string{"native.cgroupdriver=cgroupfs"}}
	for _, m := range mirrors {
		cfg.RegistryMirrors = append(cfg.RegistryMirrors, m.URL)
		if m.HTTP {
			cfg.InsecureRegistries = append(cfg.InsecureRegistries, m.Host)
		}
	}
	b, _ := json.Marshal(cfg) // a fixed struct of strings: cannot fail
	return append(b, '\n')
}

// rootlessBuildkitdConfig is the buildkitd.toml srm pins onto the pre-seeded
// builder: CDI off (see buildkitdCDIDisabled) and, with mirrors, the docker.io
// mirrors BuildKit tries for FROM and cache imports, plain http where the mirror
// is http://. BuildKit falls back to docker.io when the mirrors fail. Hosts are
// validated (config.ParseRegistryMirror), so quoting them is enough.
func rootlessBuildkitdConfig(mirrors []config.RegistryMirror) string {
	var b strings.Builder
	b.WriteString(buildkitdCDIDisabled)
	if len(mirrors) == 0 {
		return b.String()
	}
	hosts := make([]string, len(mirrors))
	for i, m := range mirrors {
		hosts[i] = strconv.Quote(m.Host)
	}
	fmt.Fprintf(&b, "\n[registry.\"docker.io\"]\n  mirrors = [%s]\n", strings.Join(hosts, ", "))
	for _, m := range mirrors {
		if m.HTTP {
			fmt.Fprintf(&b, "\n[registry.%s]\n  http = true\n", strconv.Quote(m.Host))
		}
	}
	return b.String()
}
