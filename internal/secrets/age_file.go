package secrets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filippo.io/age"
)

// AgeFileStore is a passphrase-encrypted secrets file (age scrypt). It holds a
// JSON map of key->value encrypted at rest and enforces 0600 permissions. This
// is the recommended headless default since it needs no OS keyring / D-Bus.
type AgeFileStore struct {
	path       string
	passphrase string
}

// NewAgeFileStore opens (or prepares) an age-encrypted secrets file. The
// passphrase typically comes from the SRM_SECRETS_PASSPHRASE environment
// variable. An empty passphrase is rejected.
func NewAgeFileStore(path, passphrase string) (*AgeFileStore, error) {
	if passphrase == "" {
		return nil, fmt.Errorf("age secrets: empty passphrase (set SRM_SECRETS_PASSPHRASE)")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	s := &AgeFileStore{path: path, passphrase: passphrase}
	// Validate that an existing file decrypts with this passphrase.
	if _, err := os.Stat(path); err == nil {
		if _, err := s.load(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *AgeFileStore) load() (map[string]string, error) {
	m := map[string]string{}
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	id, err := age.NewScryptIdentity(s.passphrase)
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(f, id)
	if err != nil {
		return nil, fmt.Errorf("decrypt secrets (wrong passphrase?): %w", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *AgeFileStore) save(m map[string]string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	rec, err := age.NewScryptRecipient(s.passphrase)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rec)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	// Write atomically with 0600 perms.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Get returns the secret for key, or ErrNotFound.
func (s *AgeFileStore) Get(key string) (string, error) {
	m, err := s.load()
	if err != nil {
		return "", err
	}
	v, ok := m[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

// Set stores (or replaces) the secret for key.
func (s *AgeFileStore) Set(key, value string) error {
	m, err := s.load()
	if err != nil {
		return err
	}
	m[key] = value
	return s.save(m)
}

// Delete removes a secret. Deleting an absent key is a no-op.
func (s *AgeFileStore) Delete(key string) error {
	m, err := s.load()
	if err != nil {
		return err
	}
	delete(m, key)
	return s.save(m)
}
