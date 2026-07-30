package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The secrets passphrase (for the age file store) is normally supplied via the
// SRM_SECRETS_PASSPHRASE environment variable. When a user creates one interactively
// during onboarding, srm persists it to a root-only file co-located with the config
// (see config.PassphraseFilePath) so later invocations and the detached ephemeral
// units - which run `srm _runner-cycle` as root and have no env var - can decrypt the
// store. The file holds only the passphrase; it is never logged.

// PassphraseFromFile reads a persisted passphrase from path, trimming trailing
// newline/whitespace. It returns ("", false) when the file is absent, unreadable, or
// empty, so callers can cleanly fall through to the next passphrase source.
func PassphraseFromFile(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	pass := strings.TrimRight(string(b), "\r\n")
	if pass == "" {
		return "", false
	}
	return pass, true
}

// ResolvePassphrase returns the effective secrets passphrase, preferring
// SRM_SECRETS_PASSPHRASE and falling back to the persisted passphrase file. The bool
// reports whether any passphrase was found.
func ResolvePassphrase(passFilePath string) (string, bool) {
	if p := os.Getenv("SRM_SECRETS_PASSPHRASE"); p != "" {
		return p, true
	}
	return PassphraseFromFile(passFilePath)
}

// PersistPassphrase writes passphrase to path with 0600 perms (root-only), creating
// the parent dir 0700 if needed. It is written atomically via a temp file + rename so
// a concurrent reader never sees a partial passphrase.
func PersistPassphrase(path, passphrase string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(passphrase+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// EncryptAppKey stores an org's App private key (PEM) into the age file at secPath,
// encrypted with the resolved passphrase, and returns the opened store so the caller
// can adopt it. The passphrase comes from SRM_SECRETS_PASSPHRASE, then the persisted
// passphrase file at passPath, then newPassphrase - which, when used, is persisted
// root-only to passPath so the detached ephemeral units and later runs can decrypt
// without an env var. The PEM is never logged.
func EncryptAppKey(secPath, passPath, org, pemContents, newPassphrase string) (*AgeFileStore, error) {
	pass, ok := ResolvePassphrase(passPath)
	if !ok {
		if strings.TrimSpace(newPassphrase) == "" {
			return nil, fmt.Errorf("a secrets passphrase is required to encrypt the App key")
		}
		if err := PersistPassphrase(passPath, newPassphrase); err != nil {
			return nil, fmt.Errorf("persist secrets passphrase: %w", err)
		}
		pass = newPassphrase
	}
	st, err := NewAgeFileStore(secPath, pass)
	if err != nil {
		return nil, err
	}
	if err := st.Set("app_key:"+org, pemContents); err != nil {
		return nil, err
	}
	return st, nil
}
