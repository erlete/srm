package runner

import "testing"

func TestDownloadURL(t *testing.T) {
	got := DownloadURL("2.335.1")
	want := "https://github.com/actions/runner/releases/download/v2.335.1/actions-runner-linux-x64-2.335.1.tar.gz"
	if got != want {
		t.Fatalf("DownloadURL = %q, want %q", got, want)
	}
}

func TestRenderDotEnv(t *testing.T) {
	out := RenderDotEnv(ProxyConfig{HTTPSProxy: "http://proxy.local:8080", NoProxy: "github.example"},
		map[string]string{"FOO": "bar"})
	want := "https_proxy=http://proxy.local:8080\nno_proxy=github.example\nFOO=bar\n"
	if out != want {
		t.Fatalf("RenderDotEnv =\n%q\nwant\n%q", out, want)
	}
}
