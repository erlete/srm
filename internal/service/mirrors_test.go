package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/erlete/srm/internal/config"
)

func TestRegistryMirrorCheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/" {
			t.Errorf("probed %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer auth.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer broken.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()

	m := &Manager{cfg: &config.Config{Docker: config.DockerConfig{
		RootlessDinD:    true,
		RegistryMirrors: []string{ok.URL, auth.URL, broken.URL, downURL, "not-a-url"},
	}}}
	rep := m.RegistryMirrorCheck(context.Background())
	if !rep.Configured() || !rep.DinD || len(rep.Problems) != 1 || len(rep.Mirrors) != 4 {
		t.Fatalf("report: %+v", rep)
	}
	want := []bool{true, true, false, false}
	for i, st := range rep.Mirrors {
		if st.OK != want[i] {
			t.Errorf("%s: ok=%v (%s), want %v", st.URL, st.OK, st.Detail, want[i])
		}
	}
	if !strings.Contains(rep.Mirrors[2].Detail, "502") {
		t.Errorf("status not reported: %q", rep.Mirrors[2].Detail)
	}
	if (MirrorReport{}).Configured() {
		t.Error("empty report claims mirrors")
	}
}
