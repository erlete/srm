package runner

import "testing"

func TestDownloadURL(t *testing.T) {
	got := DownloadURL("2.335.1")
	want := "https://github.com/actions/runner/releases/download/v2.335.1/actions-runner-linux-x64-2.335.1.tar.gz"
	if got != want {
		t.Fatalf("DownloadURL = %q, want %q", got, want)
	}
}

func TestVersionFromURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Round-trips the canonical URL DownloadURL builds.
		{DownloadURL("2.335.1"), "2.335.1"},
		{DownloadURL("2.300.0"), "2.300.0"},
		// Bare asset name (no host/path) is accepted.
		{AssetName("2.328.0"), "2.328.0"},
		// Tolerates a query/fragment suffix on the URL.
		{DownloadURL("2.335.1") + "?token=abc", "2.335.1"},
		// Unrecognised shapes yield "" (caller treats version as unknown).
		{"actions-runner-osx-x64-2.335.1.tar.gz", ""},     // wrong os/arch
		{"actions-runner-linux-arm64-2.335.1.tar.gz", ""}, // wrong arch
		{"https://example.com/something-else.tar.gz", ""}, // not a runner asset
		{"actions-runner-linux-x64-2.335.1.zip", ""},      // wrong suffix
		{"", ""}, // empty
	}
	for _, c := range cases {
		if got := VersionFromURL(c.in); got != c.want {
			t.Errorf("VersionFromURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2.335.1", "2.335.1", 0},
		{"2.335.0", "2.335.1", -1},
		{"2.335.1", "2.335.0", 1},
		{"2.336.0", "2.335.9", 1},
		{"3.0.0", "2.999.999", 1},
		{"2.335", "2.335.1", -1}, // missing trailing field counts as 0
		{"2.335.1", "2.335", 1},
		{"2.300.0", "2.335.1", -1},
		// leading-zero / multi-digit numeric fields compare numerically, not lexically.
		{"2.9.0", "2.10.0", -1},
		// unparseable fields sort as 0 (equal) so the guard never wrongly refuses.
		{"x.y.z", "2.335.1", -1}, // 0.0.0 < 2.335.1
		{"garbage", "garbage", 0},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
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
