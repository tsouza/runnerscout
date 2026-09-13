package provider

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

func credentialConfig(kind string) Config {
	return Config{Kind: kind, Owner: "test", Subnet: "private-subnet", SecurityGroup: "private-sg", AccountID: "000000000000", Project: "test-project", Subscription: "test-subscription", ResourceGroup: "test-group", SSHPublicKey: "ssh-ed25519 fixture"}
}

// This subprocess endpoint reports the environment actually received by exec,
// not a separately assembled expected environment.
func TestCredentialEnvironmentHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "runnerscout-env-helper" {
		return
	}
	values := map[string]string{}
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE", "AWS_EC2_METADATA_DISABLED", "AZURE_CLIENT_SECRET", "GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "HOME", "CLOUDSDK_CONFIG", "PYTHONPATH", "ENV"} {
		if value, ok := os.LookupEnv(name); ok {
			values[name] = value
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(values); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestProviderCredentialScopesSeparateNamedIdentities(t *testing.T) {
	for key, value := range map[string]string{"AWS_ACCESS_KEY_ID": "ambient-id", "AWS_SECRET_ACCESS_KEY": "ambient-secret", "AWS_SESSION_TOKEN": "ambient-session", "AZURE_CLIENT_SECRET": "ambient-azure", "GOOGLE_APPLICATION_CREDENTIALS": "/ambient/gcp.json", "CLOUDSDK_CONFIG": "/ambient/cache", "PYTHONPATH": "/ambient/python", "ENV": "/ambient/shell"} {
		t.Setenv(key, value)
	}
	firstValues := map[string]string{"AWS_ACCESS_KEY_ID": "first-id", "AWS_SECRET_ACCESS_KEY": "first-secret"}
	first, cleanFirst, err := NewCommand(credentialConfig("aws"), firstValues)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanFirst() })
	firstValues["AWS_SECRET_ACCESS_KEY"] = "changed-after-construction"
	second, cleanSecond, err := NewCommand(credentialConfig("aws"), map[string]string{"AWS_ACCESS_KEY_ID": "second-id", "AWS_SECRET_ACCESS_KEY": "second-secret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanSecond() })
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commands := []*Command{first, second}
	results := make([]map[string]string, len(commands))
	errors := make([]error, len(commands))
	var wg sync.WaitGroup
	for i, command := range commands {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := command.Exec.Run(ctx, binary, "-test.run=^TestCredentialEnvironmentHelper$", "--", "runnerscout-env-helper")
			if err == nil {
				err = json.Unmarshal(out, &results[i])
			}
			errors[i] = err
		}()
	}
	wg.Wait()
	homes := map[string]bool{}
	for i, result := range results {
		if errors[i] != nil {
			t.Fatal("credential subprocess failed", errors[i])
		}
		home := result["HOME"]
		info, err := os.Stat(home)
		if err != nil || info.Mode().Perm() != 0700 || homes[home] {
			t.Fatal("credential scope lacks a unique private directory")
		}
		homes[home] = true
		for _, forbidden := range []string{"AZURE_CLIENT_SECRET", "AWS_SESSION_TOKEN", "PYTHONPATH", "ENV"} {
			if _, exists := result[forbidden]; exists {
				t.Fatalf("inherited unrelated variable %s", forbidden)
			}
		}
		if i < 2 {
			prefix := []string{"first", "second"}[i]
			if result["AWS_ACCESS_KEY_ID"] != prefix+"-id" || result["AWS_SECRET_ACCESS_KEY"] != prefix+"-secret" || result["GOOGLE_APPLICATION_CREDENTIALS"] != "" || result["CLOUDSDK_CONFIG"] != "" {
				t.Fatal("named AWS identity leaked or changed")
			}
			if result["AWS_CONFIG_FILE"] != filepath.Join(home, "config") || result["AWS_SHARED_CREDENTIALS_FILE"] != filepath.Join(home, "credentials") || result["AWS_EC2_METADATA_DISABLED"] != "true" {
				t.Fatal("AWS can read a shared default credential source")
			}
		}
		if strings.Contains(fmt.Sprintf("%v %+v %#v", commands[i].Exec, commands[i].Exec, commands[i].Exec), "-secret") {
			t.Fatal("executor diagnostics exposed credentials")
		}
	}
	if os.Getenv("AWS_SECRET_ACCESS_KEY") != "ambient-secret" || os.Getenv("CLOUDSDK_CONFIG") != "/ambient/cache" {
		t.Fatal("provider construction mutated process-wide credentials")
	}
	if err := cleanFirst(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(results[0]["HOME"]); !os.IsNotExist(err) {
		t.Fatal("closed scope retained credential cache")
	}
	if _, err := os.Stat(results[1]["HOME"]); err != nil {
		t.Fatal("closing one scope removed another")
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
			if !valid || command.Exec != nil {
				t.Fatal("Azure credential selection used another mechanism or a CLI")
			}
		})
	}
}
