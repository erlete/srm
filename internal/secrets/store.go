// Package secrets stores and retrieves sensitive values (PATs, App private
// keys) behind a pluggable Store. The headless-friendly default is an
// age-encrypted file (see age_file.go); EnvStore is a zero-setup fallback.
//
// Short-lived GitHub tokens (registration/remove tokens, ~1h) are NEVER stored
// here - they are minted on demand and discarded.
package secrets

import (
	"fmt"
	"os"
	"strings"
)

// Store is the interface every secret backend implements.
type Store interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
}

// ErrNotFound is returned by Get when a key is absent.
var ErrNotFound = fmt.Errorf("secret not found")

// EnvStore resolves secrets from environment variables. A key like "pat:acme"
// maps to env var SRM_SECRET_PAT_ACME (uppercased, non-alphanumerics -> "_").
// It is read-only; Set/Delete return an error.
type EnvStore struct{}

func (EnvStore) envName(key string) string {
	var b strings.Builder
	b.WriteString("SRM_SECRET_")
	for _, r := range strings.ToUpper(key) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func (s EnvStore) Get(key string) (string, error) {
	if v, ok := os.LookupEnv(s.envName(key)); ok {
		return v, nil
	}
	return "", ErrNotFound
}

func (EnvStore) Set(string, string) error {
	return fmt.Errorf("EnvStore is read-only")
}

func (EnvStore) Delete(string) error {
	return fmt.Errorf("EnvStore is read-only")
}

// Redact masks a secret for safe logging/display, showing only enough to
// identify it. Use it everywhere a secret might reach logs or the TUI.
func Redact(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "********"
	}
	return s[:4] + "…" + s[len(s)-2:]
}
