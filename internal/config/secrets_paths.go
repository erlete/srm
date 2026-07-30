package config

import "path/filepath"

// The age secrets file and the root-only passphrase file are co-located with the
// active config file, so a managed host's config dir (/etc/srm on Linux,
// %ProgramData%\srm on Windows) holds config.yaml, secrets.age, and secrets.pass
// together. These are the single source of truth both the CLI and the service layer
// derive from, so an interactively-created passphrase written by one is found by the
// other (and by the detached ephemeral units running `srm _runner-cycle`).

// SecretsFilePath returns the age secrets file co-located with cfgPath
// (<dir>/secrets.age). An empty cfgPath yields "".
func SecretsFilePath(cfgPath string) string {
	if cfgPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(cfgPath), "secrets.age")
}

// PassphraseFilePath returns the root-only passphrase file co-located with cfgPath
// (<dir>/secrets.pass). It persists an interactively-created secrets passphrase so
// later runs and the detached ephemeral units can decrypt the store without an env
// var. An empty cfgPath yields "".
func PassphraseFilePath(cfgPath string) string {
	if cfgPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(cfgPath), "secrets.pass")
}
