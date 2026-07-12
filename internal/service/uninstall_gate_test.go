package service

import "testing"

// PurgeRemovesConfig is the gate that decides whether the extra purge confirmations
// (a second typed token, the writable-backup pre-check) apply: only a full-host purge
// that does not keep the config dir actually deletes /etc/srm.
func TestPurgeRemovesConfig(t *testing.T) {
	cases := []struct {
		name string
		opts UninstallOpts
		want bool
	}{
		{"full-host purge", UninstallOpts{Purge: true}, true},
		{"no purge", UninstallOpts{}, false},
		{"purge but keep config", UninstallOpts{Purge: true, KeepConfig: true}, false},
		{"org-scoped purge", UninstallOpts{Purge: true, Org: "acme"}, false},
	}
	for _, c := range cases {
		if got := c.opts.PurgeRemovesConfig(); got != c.want {
			t.Errorf("%s: PurgeRemovesConfig() = %v, want %v", c.name, got, c.want)
		}
	}
}
