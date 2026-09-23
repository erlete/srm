package cli

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

// validatePEMPrivateKey accepts a real RSA key in either PKCS#1 or PKCS#8 PEM form
// and rejects non-PEM bytes, a PEM block that is not a private key, and empty input.
func TestValidatePEMPrivateKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes})

	valid := map[string][]byte{
		"pkcs1": pkcs1,
		"pkcs8": pkcs8,
	}
	for name, b := range valid {
		if err := validatePEMPrivateKey(b); err != nil {
			t.Errorf("%s: unexpected error: %v", name, err)
		}
	}

	invalid := map[string][]byte{
		"empty":          nil,
		"not-pem":        []byte("just some text, definitely not a key"),
		"cert-not-key":   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("nope")}),
		"garbage-in-pem": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("garbage")}),
	}
	for name, b := range invalid {
		if err := validatePEMPrivateKey(b); err == nil {
			t.Errorf("%s: expected an error, got nil", name)
		}
	}
}
