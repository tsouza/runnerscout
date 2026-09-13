package configapi

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kt "k8s.io/client-go/testing"
)

func TestLoadRechecksConfigurationAfterReadingSecrets(t *testing.T) {
	reader := readerFixture(t)
	_, client := credentialFixture(t)
	client.PrependReactor("get", "secrets", func(kt.Action) (bool, runtime.Object, error) {
		reader.objects["runnerclasses/linux"].SetResourceVersion("changed-during-secret-read")
		return false, nil, nil
	})
	loaded, err := Load(context.Background(), reader, client.CoreV1().Secrets("test"), "test", "build")
	if !errors.Is(err, ErrChanged) || len(loaded.Credentials.GitHub) != 0 || len(loaded.Credentials.Providers) != 0 {
		t.Fatal("mixed configuration and credential snapshot escaped", err)
	}
}

func TestLoadedKeyIgnoresStatusButDetectsIdentityAndSecretRotation(t *testing.T) {
	ctx := context.Background()
	reader := readerFixture(t)
	_, client := credentialFixture(t)
	load := func() Loaded {
		t.Helper()
		value, err := Load(ctx, reader, client.CoreV1().Secrets("test"), "test", "build")
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	first := load()
	reader.objects["runnerscalesets/build"].SetResourceVersion("status-write")
	reader.objects["runnerscalesets/build"].Object["status"] = map[string]any{"observedGeneration": int64(1)}
	if next := load(); next.Key() != first.Key() {
		t.Fatal("status writes would restart the listener forever")
	}
	secret, err := client.CoreV1().Secrets("test").Get(ctx, "github", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret.ResourceVersion = "2"
	secret.Data["token"] = []byte("rotated-fixture-token")
	if _, err := client.CoreV1().Secrets("test").Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	rotated := load()
	if rotated.Key() == first.Key() {
		t.Fatal("Secret rotation was invisible to runtime reload")
	}
	reader.objects["providerconfigs/aws"].SetUID("recreated-provider")
	if recreated := load(); recreated.Key() == rotated.Key() {
		t.Fatal("provider recreation was invisible to runtime reload")
	}
}

func TestCleanupCredentialsDoNotRequireGitHubSecret(t *testing.T) {
	resolved, client := credentialFixture(t)
	if err := client.CoreV1().Secrets("test").Delete(context.Background(), "github", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	client.ClearActions()
	credentials, err := ResolveCleanupSecrets(context.Background(), client.CoreV1().Secrets("test"), resolved)
	if err != nil || len(credentials.GitHub) != 0 || credentials.Providers["aws"]["AWS_ACCESS_KEY_ID"] == "" {
		t.Fatal("cloud cleanup incorrectly required GitHub credentials", err)
	}
	for _, action := range client.Actions() {
		if get, ok := action.(kt.GetAction); ok && get.GetName() == "github" {
			t.Fatal("cleanup read the GitHub Secret")
		}
	}
	if normal, err := ResolveSecrets(context.Background(), client.CoreV1().Secrets("test"), resolved); err == nil || len(normal.Providers) != 0 {
		t.Fatal("normal execution accepted missing GitHub credentials")
	}
}
