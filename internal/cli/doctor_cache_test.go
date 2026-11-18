package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/erlete/srm/internal/config"
)

func doctorStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	f()
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestPrintCacheEnv(t *testing.T) {
	cfg := &config.Config{Orgs: []config.OrgConfig{
		{Name: "plain"},
		{Name: "tuned", Cache: config.OrgCache{Env: map[string]string{
			"TURBO_CACHE_DIR": "turbo", "UV_CACHE_DIR": "py/uv", "GOCACHE": "", "HOME": "x",
		}}},
	}}
	n := len(config.ToolCacheEnv("/r", nil))

	out := doctorStdout(t, func() { printCacheEnv(cfg, "plain") })
	if strings.Contains(out, "cache.env") || !strings.Contains(out, "vars under") {
		t.Errorf("plain org: %q", out)
	}

	out = doctorStdout(t, func() { printCacheEnv(cfg, "tuned") })
	if !strings.Contains(out, "(cache.env: -GOCACHE +TURBO_CACHE_DIR ~UV_CACHE_DIR)") {
		t.Errorf("changes not listed: %q", out)
	}
	if !strings.Contains(out, "IGNORED cache.env HOME: reserved") {
		t.Errorf("invalid entry not reported: %q", out)
	}
	if !strings.Contains(out, fmt.Sprintf("cache env:   %d vars", n)) { // -1 removed +1 added
		t.Errorf("var count: %q", out)
	}
}
