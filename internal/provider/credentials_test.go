package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/tsouza/runnerscout/internal/testutil"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

func credentialConfig(kind string) Config {
	return Config{Kind: kind, Owner: "test", Subnet: "private-subnet", SecurityGroup: "private-sg", AccountID: "000000000000", Project: "test-project", Subscription: "test-subscription", ResourceGroup: "test-group", SSHPublicKey: "ssh-ed25519 fixture"}
}

func TestProviderCredentialScopesSeparateNamedIdentities(t *testing.T) {
	for key, value := range map[string]string{"AWS_ACCESS_KEY_ID": "ambient-id", "AWS_SECRET_ACCESS_KEY": "ambient-secret", "AWS_SESSION_TOKEN": "ambient-session", "AZURE_CLIENT_SECRET": "ambient-azure", "GOOGLE_APPLICATION_CREDENTIALS": "/ambient/gcp.json"} {
		t.Setenv(key, value)
	}
	var wg sync.WaitGroup
	for _, prefix := range []string{"first", "second"} {
		values := map[string]string{"AWS_ACCESS_KEY_ID": prefix + "-id", "AWS_SECRET_ACCESS_KEY": prefix + "-secret"}
		command, cleanup, err := NewCommand(credentialConfig("aws"), values)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cleanup() })
		values["AWS_SECRET_ACCESS_KEY"] = "changed-after-construction"
		fixture := testutil.NewAWS(t)
		var identities atomic.Int64
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			authorization := r.Header.Get("Authorization")
			if !strings.Contains(authorization, "Credential="+prefix+"-id/") || r.Header.Get("X-Amz-Security-Token") != "" {
				t.Error("wrong or ambient credential signed request")
			}
			signed := strings.Split(strings.Split(authorization, "SignedHeaders=")[1], ",")[0]
			reconstructed, err := http.NewRequestWithContext(r.Context(), r.Method, "https://"+r.Host+r.URL.RequestURI(), bytes.NewReader(body))
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			for _, name := range strings.Split(signed, ";") {
				if name != "host" {
					reconstructed.Header[http.CanonicalHeaderKey(name)] = append([]string{}, r.Header.Values(name)...)
				}
			}
			when, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			form, err := url.ParseQuery(string(body))
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			service := "ec2"
			if form.Get("Action") == "GetCallerIdentity" {
				service = "sts"
				identities.Add(1)
			}
			hash := sha256.Sum256(body)
			err = v4.NewSigner().SignHTTP(r.Context(), aws.Credentials{AccessKeyID: prefix + "-id", SecretAccessKey: prefix + "-secret"}, reconstructed, hex.EncodeToString(hash[:]), service, "us-east-1", when)
			if err != nil || reconstructed.Header.Get("Authorization") != authorization {
				t.Error("request was not signed with the original isolated secret", err)
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			fixture.Server.Config.Handler.ServeHTTP(w, r)
		}))
		t.Cleanup(server.Close)
		command.AWS.HTTPClient, command.AWS.Endpoint = server.Client(), server.URL
		if strings.Contains(fmt.Sprintf("%v %+v %#v", command.AWS, command.AWS, command.AWS), "-secret") {
			t.Fatal("SDK diagnostics expose secret")
		}
		wg.Go(func() {
			for range 3 {
				ob, err := command.Observe(context.Background(), allocation())
				if err != nil || !ob.Known || ob.Exists {
					t.Error("isolated observation failed", ob, err)
				}
			}
			if identities.Load() != 3 {
				t.Error("account was not checked per operation")
			}
		})
	}
	wg.Wait()
	if os.Getenv("AWS_SECRET_ACCESS_KEY") != "ambient-secret" || os.Getenv("AWS_SESSION_TOKEN") != "ambient-session" {
		t.Fatal("provider changed process credentials")
	}
}

func TestProviderCredentialScopesRejectPartialAndCrossProviderSettings(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	t.Setenv("AZURE_TENANT_ID", "ambient-tenant")
	t.Setenv("AZURE_CLIENT_ID", "ambient-client")
	for _, tc := range []struct {
		kind string
		env  map[string]string
	}{
		{"aws", map[string]string{"AWS_ACCESS_KEY_ID": "partial-id"}},
		{"aws", map[string]string{"AWS_SESSION_TOKEN": "partial-session"}},
		{"aws", map[string]string{"AWS_ROLE_ARN": "partial-role"}},
		{"aws", map[string]string{"AWS_ACCESS_KEY_ID": "id", "AWS_SECRET_ACCESS_KEY": "secret", "AWS_WEB_IDENTITY_TOKEN_FILE": "/token", "AWS_ROLE_ARN": "role"}},
		{"aws", map[string]string{"AZURE_CLIENT_SECRET": "cross-provider-secret"}},
		{"gcp", map[string]string{"CLOUDSDK_CONFIG": "/shared/cache"}},
		{"gcp", map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "/one", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE": "/two"}},
		{"aws", map[string]string{"PATH": "/attacker"}},
		{"aws", map[string]string{"AWS_ACCESS_KEY_ID": "invalid\x00id"}},
		{"azure", map[string]string{"AZURE_CLIENT_SECRET": "partial-secret"}},
		{"azure", map[string]string{"AZURE_CLIENT_ID": "client", "AZURE_TENANT_ID": "tenant", "AZURE_CLIENT_SECRET": "secret", "AZURE_FEDERATED_TOKEN_FILE": "/token"}},
		{"azure", map[string]string{"AZURE_CLIENT_CERTIFICATE_PASSWORD": "orphan-password"}},
	} {
		command, cleanup, err := NewCommand(credentialConfig(tc.kind), tc.env)
		if err == nil || command != nil || cleanup != nil {
			t.Fatal("invalid credential scope accepted")
		}
		if strings.Contains(err.Error(), "partial-secret") || strings.Contains(err.Error(), "cross-provider-secret") {
			t.Fatal("credential value leaked in diagnostics")
		}
	}
}

func TestAzureNamedCredentialsUseExplicitSDKMechanisms(t *testing.T) {
	t.Setenv("AZURE_CLIENT_SECRET", "unrelated-ambient-secret")
	t.Setenv("AZURE_TENANT_ID", "unrelated-ambient-tenant")
	t.Setenv("AZURE_FEDERATED_TOKEN_FILE", "/unrelated/token")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "identity.pem")
	data := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"secret", "workload", "certificate", "managed"} {
		t.Run(mode, func(t *testing.T) {
			env := map[string]string{"AZURE_CLIENT_ID": "explicit-client", "AZURE_TENANT_ID": "explicit-tenant"}
			switch mode {
			case "secret":
				env["AZURE_CLIENT_SECRET"] = "explicit-secret"
			case "workload":
				env["AZURE_FEDERATED_TOKEN_FILE"] = "/mounted/token"
			case "certificate":
				env["AZURE_CLIENT_CERTIFICATE_PATH"] = path
			case "managed":
				delete(env, "AZURE_TENANT_ID")
			}
			command, cleanup, err := NewCommand(credentialConfig("azure"), env)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			credential, err := command.Azure.credentials()
			if err != nil {
				t.Fatal(err)
			}
			valid := false
			switch mode {
			case "secret":
				_, valid = credential.(*azidentity.ClientSecretCredential)
			case "workload":
				_, valid = credential.(*azidentity.WorkloadIdentityCredential)
			case "certificate":
				_, valid = credential.(*azidentity.ClientCertificateCredential)
			case "managed":
				_, valid = credential.(*azidentity.ManagedIdentityCredential)
			}
			if !valid {
				t.Fatal("Azure credential selection used another mechanism or a CLI")
			}
		})
	}
}
