package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// GCPServiceAccountFixture writes a syntactically valid, never-dialed GCP
// service-account credential file and returns its path, so
// GOOGLE_APPLICATION_CREDENTIALS-based SDK construction succeeds fully
// offline (parsing a service-account credential validates its RSA key
// locally; no network call happens until something later calls Token()).
// Mirrors internal/provider/gcp_credentials_test.go's own fixture shape.
func GCPServiceAccountFixture(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"client_email": "fixture@fixture.invalid",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})),
		"token_uri":    "https://fixture.invalid/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "gcp-credentials.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
