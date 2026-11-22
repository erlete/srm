package runner

import (
	"testing"

	"github.com/erlete/srm/internal/config"
)

// No mirrors: daemon.json stays exactly what srm always wrote.
func TestRootlessDaemonConfigWithoutMirrors(t *testing.T) {
	want := `{"exec-opts":["native.cgroupdriver=cgroupfs"]}` + "\n"
	if got := string(rootlessDaemonConfig(nil)); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := rootlessBuildkitdConfig(nil); got != buildkitdCDIDisabled {
		t.Errorf("buildkitd.toml changed without mirrors: %q", got)
	}
}

func TestRootlessConfigsWithMirrors(t *testing.T) {
	ms, probs := config.ParseRegistryMirrors([]string{"http://10.255.255.1:5000", "https://mirror.example"})
	if len(probs) != 0 {
		t.Fatal(probs)
	}
	wantJSON := `{"exec-opts":["native.cgroupdriver=cgroupfs"],` +
		`"registry-mirrors":["http://10.255.255.1:5000","https://mirror.example"],` +
		`"insecure-registries":["10.255.255.1:5000"]}` + "\n"
	if got := string(rootlessDaemonConfig(ms)); got != wantJSON {
		t.Errorf("daemon.json:\n got %s\nwant %s", got, wantJSON)
	}
	wantTOML := buildkitdCDIDisabled + `
[registry."docker.io"]
  mirrors = ["10.255.255.1:5000", "mirror.example"]

[registry."10.255.255.1:5000"]
  http = true
`
	if got := rootlessBuildkitdConfig(ms); got != wantTOML {
		t.Errorf("buildkitd.toml:\n got %q\nwant %q", got, wantTOML)
	}
}
