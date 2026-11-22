package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/erlete/srm/internal/config"
)

// MirrorStatus is one docker.registryMirrors entry as `srm doctor` checks it.
type MirrorStatus struct {
	URL    string
	OK     bool
	Detail string // "GET /v2/ -> 200", or why it failed
}

// MirrorReport is the configured registry mirrors and whether each answers.
type MirrorReport struct {
	DinD     bool // docker.rootlessDinD on: the mirrors are only used by DinD slots
	Mirrors  []MirrorStatus
	Problems []string // entries left out (invalid)
}

// Configured reports whether docker.registryMirrors has any entry.
func (r MirrorReport) Configured() bool { return len(r.Mirrors)+len(r.Problems) > 0 }

// mirrorClient probes mirrors; a seam for tests.
var mirrorClient = &http.Client{Timeout: 5 * time.Second}

// RegistryMirrorCheck probes each valid mirror with GET /v2/, the registry API
// base: 200 or 401 (auth required) means a registry answers. The probe runs from
// the host, like the slots' dockerd; BuildKit reaches the mirror from its
// container's network, which `srm doctor` cannot test.
func (m *Manager) RegistryMirrorCheck(ctx context.Context) MirrorReport {
	mirrors, problems := config.ParseRegistryMirrors(m.cfg.Docker.RegistryMirrors)
	rep := MirrorReport{DinD: m.cfg.Docker.RootlessDinD, Problems: problems}
	for _, mr := range mirrors {
		rep.Mirrors = append(rep.Mirrors, probeMirror(ctx, mr.URL))
	}
	return rep
}

func probeMirror(ctx context.Context, base string) MirrorStatus {
	st := MirrorStatus{URL: base}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/", nil)
	if err != nil {
		st.Detail = err.Error()
		return st
	}
	resp, err := mirrorClient.Do(req)
	if err != nil {
		st.Detail = err.Error()
		return st
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	st.Detail = fmt.Sprintf("GET /v2/ -> %d", resp.StatusCode)
	st.OK = resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized
	return st
}
