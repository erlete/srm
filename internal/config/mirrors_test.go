package config

import "testing"

func TestParseRegistryMirror(t *testing.T) {
	for in, want := range map[string]RegistryMirror{
		"http://10.255.255.1:5000":    {URL: "http://10.255.255.1:5000", Host: "10.255.255.1:5000", HTTP: true},
		" http://10.255.255.1:5000/ ": {URL: "http://10.255.255.1:5000", Host: "10.255.255.1:5000", HTTP: true},
		"https://mirror.gcr.io":       {URL: "https://mirror.gcr.io", Host: "mirror.gcr.io"},
	} {
		got, err := ParseRegistryMirror(in)
		if err != nil || got != want {
			t.Errorf("%q = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"10.255.255.1:5000",      // no scheme
		"ftp://mirror",           // scheme
		"http://mirror/v2",       // path
		"http://user:pw@mirror",  // credentials
		"http://mirror?x=1",      // query
		"http://mir ror",         // host
		`http://mirror"],"x":["`, // JSON/TOML injection
		"http://mirror:99999",    // port
		"http://[::1]:5000",      // IPv6 literal (not supported)
	} {
		if _, err := ParseRegistryMirror(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseRegistryMirrorsKeepsGoodOnes(t *testing.T) {
	ms, probs := ParseRegistryMirrors([]string{"http://a:5000", "bogus", "http://a:5000/", "https://b"})
	if len(ms) != 2 || ms[0].Host != "a:5000" || ms[1].Host != "b" {
		t.Errorf("mirrors: %+v", ms)
	}
	if len(probs) != 1 {
		t.Errorf("problems: %v", probs)
	}
}
