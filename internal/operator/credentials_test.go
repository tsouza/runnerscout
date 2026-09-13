package operator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/provider"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func namedCredentialConfig() Config {
	return Config{Name: "test", Namespace: "test", GitHubURL: "https://github.com/tsouza/runnerscout", ScaleSetID: 1, MaxRunners: 2, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600,
		Requirements: placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: []string{"a-aws", "z-gcp"}, Regions: []string{"r"}, Policy: "lowest-price"},
		Providers: map[string]provider.Config{
			"a-aws": {Kind: "aws", Owner: "test", AccountID: "000000000000", Subnet: "private", SecurityGroup: "private"},
			"z-gcp": {Kind: "gcp", Owner: "test", Project: "test-project", Subnet: "private"},
		}}
}

func TestOperatorNamedCredentialsStayOutOfDurableState(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	credentialFile := filepath.Join(t.TempDir(), "gcp.json")
	if err := os.WriteFile(credentialFile, []byte(`{"type":"authorized_user","client_id":"fixture","client_secret":"fixture-gcp-client-secret","refresh_token":"fixture-refresh"}`), 0600); err != nil {
		t.Fatal(err)
	}

	client := fake.NewClientset()
	credentials := map[string]map[string]string{
		"a-aws": {"AWS_ACCESS_KEY_ID": "fixture-access-id", "AWS_SECRET_ACCESS_KEY": "fixture-access-secret"},
		"z-gcp": {"GOOGLE_APPLICATION_CREDENTIALS": credentialFile},
	}
	op, cleanup, err := NewWithCredentials(namedCredentialConfig(), client, nil, credentials)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup() })
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 1 {
		t.Fatal("provider scopes not independently created")
	}
	for _, command := range op.Controller.Providers {
		p, ok := command.(*provider.Command)
		if !ok || p.Bootstrap == nil || (p.Config.Kind == "aws" && p.Exec == nil) || (p.Config.Kind == "gcp" && (p.GCP == nil || p.Exec != nil)) {
			t.Fatal("provider lost its runtime wiring")
		}
	}
	if _, err := op.HandleDesiredRunnerCount(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	state, err := client.CoreV1().ConfigMaps("test").Get(context.Background(), "test-fleet", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"fixture-access-id", "fixture-access-secret", credentialFile, "fixture-gcp-client-secret", "fixture-refresh", tmp} {
		if strings.Contains(string(data), value) {
			t.Fatal("credential material or cache path persisted to Kubernetes")
		}
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(credentialFile); err != nil {
		t.Fatal("cleanup removed caller-owned credentials", err)
	}

	entries, err = os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatal("credential caches retained after controller shutdown")
	}
}

func TestOperatorCredentialFailureRollsBackScopes(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	credentials := map[string]map[string]string{
		"a-aws": {"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"},
		"z-gcp": {"GOOGLE_APPLICATION_CREDENTIALS": "/one", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE": "/two"},
	}
	op, cleanup, err := NewWithCredentials(namedCredentialConfig(), fake.NewClientset(), nil, credentials)
	if err == nil || op != nil || cleanup != nil {
		t.Fatal("inconsistent credentials produced a runnable controller")
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed startup retained an earlier provider credential cache")
	}
	credentials = map[string]map[string]string{"unconfigured": {"AWS_ACCESS_KEY_ID": "fixture-id"}}
	if op, cleanup, err := NewWithCredentials(namedCredentialConfig(), fake.NewClientset(), nil, credentials); err == nil || op != nil || cleanup != nil {
		t.Fatal("unconfigured credential provider accepted")
	}
}
