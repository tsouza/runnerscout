//go:build integration

package configapi

import (
	"context"
	"errors"
	"sync"
	"testing"

	api "github.com/tsouza/runnerscout/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
)

// Rotate through the real API after the first read but before the resolver's
// second collection. The returned old object reproduces a mixed snapshot.
type rotatingRealSecrets struct {
	typed.SecretInterface
	name string
	once sync.Once
}

func (r *rotatingRealSecrets) Get(ctx context.Context, name string, options metav1.GetOptions) (*corev1.Secret, error) {
	object, err := r.SecretInterface.Get(ctx, name, options)
	if err != nil || name != r.name {
		return object, err
	}
	r.once.Do(func() {
		changed := object.DeepCopy()
		for key := range changed.Data {
			changed.Data[key] = []byte("rotated-fixture-token")
		}
		_, err = r.SecretInterface.Update(ctx, changed, metav1.UpdateOptions{})
	})
	return object, err
}

func verifyRealSecretResolution(t *testing.T, ctx context.Context, kc kubernetes.Interface, dc dynamic.Interface, namespace string) {
	t.Helper()
	secrets := kc.CoreV1().Secrets(namespace)
	if _, err := secrets.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github", Namespace: namespace}, Data: map[string][]byte{"token": []byte("fixture-token\n"), "unreferenced": []byte("must-not-resolve")}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	values := map[string]map[string]string{
		"aws":   {"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"},
		"azure": {"AZURE_CLIENT_ID": "fixture-client", "AZURE_TENANT_ID": "fixture-tenant", "AZURE_CLIENT_SECRET": "fixture-secret"},
		"gcp":   {"GOOGLE_APPLICATION_CREDENTIALS": "/mounted/fixture.json"},
	}
	providers := dc.Resource(schema.GroupVersionResource{Group: api.Group, Version: api.Version, Resource: "providerconfigs"}).Namespace(namespace)
	for name, environment := range values {
		data, references := map[string][]byte{}, map[string]any{}
		for key, value := range environment {
			data[key] = []byte(value)
			references[key] = map[string]any{"name": name + "-identity", "key": key}
		}
		if _, err := secrets.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-identity", Namespace: namespace}, Data: data}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		object, err := providers.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		object.Object["spec"].(map[string]any)["credentialEnvironment"] = references
		if _, err := providers.Update(ctx, object, metav1.UpdateOptions{FieldValidation: "Strict"}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, _, err := Read(ctx, KubernetesReader{Client: dc}, namespace, "build")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := Compile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := ResolveSecrets(ctx, secrets, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if string(credentials.GitHub) != "fixture-token" || len(credentials.Providers) != len(values) {
		t.Fatal("real Secret snapshot lost authentication material")
	}
	for name, environment := range values {
		if len(credentials.Providers[name]) != len(environment) {
			t.Fatal("unreferenced Secret key escaped resolution")
		}
		for key, value := range environment {
			if credentials.Providers[name][key] != value {
				t.Fatal("real provider Secret did not resolve")
			}
		}
	}
	rotating := &rotatingRealSecrets{SecretInterface: secrets, name: "github"}
	if credentials, err := ResolveSecrets(ctx, rotating, resolved); !errors.Is(err, ErrChanged) || len(credentials.GitHub) != 0 || len(credentials.Providers) != 0 {
		t.Fatal("concurrent real Secret rotation returned partial material", err)
	}
	credentials, err = ResolveSecrets(ctx, secrets, resolved)
	if err != nil || string(credentials.GitHub) != "rotated-fixture-token" {
		t.Fatal("fresh real Secret rotation was not adopted", err)
	}
}
