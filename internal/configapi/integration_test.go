//go:build integration

package configapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	api "github.com/tsouza/runnerscout/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

// This test requires an explicitly isolated cluster. Missing configuration is a
// failure, never a skip or a fallback to the developer's default cluster.
func TestRealKubernetesCRDSchemasAndConfigurationSnapshot(t *testing.T) {
	path := os.Getenv("RUNNERSCOUT_TEST_KUBECONFIG")
	if path == "" {
		t.Fatal("explicit isolated RUNNERSCOUT_TEST_KUBECONFIG required")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Timeout = 15 * time.Second
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	kc, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	crds := dc.Resource(schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"})
	files, err := filepath.Glob("../../config/crd/bases/*.yaml")
	if err != nil || len(files) != 5 {
		t.Fatalf("expected all five CRD schemas: %v", err)
	}
	var created []string
	namespace := "runnerscout-crd-" + uuid.NewString()[:8]
	namespaceCreated := false
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Minute)
		defer stop()
		if namespaceCreated {
			if err := kc.CoreV1().Namespaces().Delete(cleanup, namespace, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("namespace cleanup: %v", err)
			}
			if err := wait.PollUntilContextCancel(cleanup, time.Second, true, func(ctx context.Context) (bool, error) {
				_, err := kc.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					return true, nil
				}
				return false, err
			}); err != nil {
				t.Errorf("namespace absence not confirmed: %v", err)
			}
		}
		for _, name := range created {
			if err := crds.Delete(cleanup, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("CRD cleanup: %v", err)
			}
			if err := wait.PollUntilContextCancel(cleanup, time.Second, true, func(ctx context.Context) (bool, error) {
				_, err := crds.Get(ctx, name, metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					return true, nil
				}
				return false, err
			}); err != nil {
				t.Errorf("CRD absence not confirmed: %v", err)
			}
		}
	}()
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data, err = yaml.YAMLToJSON(data)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err = json.Unmarshal(data, &object); err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{Object: object}
		// Never replace or delete a CRD that was present before this test.
		if _, err := crds.Get(ctx, u.GetName(), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("test CRD must not preexist: %s (%v)", u.GetName(), err)
		}
		if _, err := crds.Create(ctx, u, metav1.CreateOptions{FieldValidation: "Strict"}); err != nil {
			t.Fatal(err)
		}
		created = append(created, u.GetName())
		if err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
			current, err := crds.Get(ctx, u.GetName(), metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			conditions, _, _ := unstructured.NestedSlice(current.Object, "status", "conditions")
			for _, condition := range conditions {
				c := condition.(map[string]any)
				if c["type"] == "Established" && c["status"] == "True" {
					return true, nil
				}
			}
			return false, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := kc.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	namespaceCreated = true
	fixture := readerFixture(t)
	fixture.objects["capacitycatalogs/prices"].Object["spec"].(map[string]any)["offerings"] = []any{}
	for key, u := range fixture.objects {
		resource := ""
		for i, c := range key {
			if c == '/' {
				resource = key[:i]
				break
			}
		}
		u.SetNamespace(namespace)
		u.SetResourceVersion("")
		u.SetUID("")
		u.SetGeneration(0)
		if _, err := dc.Resource(schema.GroupVersionResource{Group: api.Group, Version: api.Version, Resource: resource}).Namespace(namespace).Create(ctx, u, metav1.CreateOptions{FieldValidation: "Strict"}); err != nil {
			t.Fatalf("create %s: %v", key, err)
		}
	}
	s, revisions, err := Read(ctx, KubernetesReader{Client: dc}, namespace, "build")
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 6 || len(s.Providers) != 3 {
		t.Fatal("real snapshot is incomplete")
	}
	verifyRealSecretResolution(t, ctx, kc, dc, namespace)
	verifyRealRuntimeLifecycle(t, ctx, kc, dc, namespace)
	scaleSets := dc.Resource(schema.GroupVersionResource{Group: api.Group, Version: api.Version, Resource: "runnerscalesets"}).Namespace(namespace)
	invalid := fixture.objects["runnerscalesets/build"].DeepCopy()
	invalid.SetName("invalid")
	invalid.Object["spec"].(map[string]any)["maxRunners"] = float64(0)
	if _, err := scaleSets.Create(ctx, invalid, metav1.CreateOptions{FieldValidation: "Strict"}); !apierrors.IsInvalid(err) {
		t.Fatalf("API server did not reject invalid admission limit: %v", err)
	}
	invalid = fixture.objects["runnerscalesets/build"].DeepCopy()
	invalid.SetName("unknown")
	invalid.Object["spec"].(map[string]any)["silentlyIgnoredOption"] = true
	if _, err := scaleSets.Create(ctx, invalid, metav1.CreateOptions{FieldValidation: "Strict"}); err == nil {
		t.Fatal("API server accepted unknown configuration field")
	}
	classes := dc.Resource(schema.GroupVersionResource{Group: api.Group, Version: api.Version, Resource: "runnerclasses"}).Namespace(namespace)
	class, err := classes.Get(ctx, "linux", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	network := api.NetworkProfile{TypeMeta: metav1.TypeMeta{APIVersion: api.Group + "/" + api.Version, Kind: "NetworkProfile"}, ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: namespace}, Spec: api.NetworkProfileSpec{Mode: "separate", Mappings: []api.NetworkMapping{
		{ProviderRef: api.LocalReference{Name: "aws"}, Region: "us-east-1", NetworkID: "vpc", SubnetID: "subnet-1", CIDRs: []string{"10.1.0.0/24"}},
		{ProviderRef: api.LocalReference{Name: "azure"}, Region: "eastus", NetworkID: "vnet", SubnetID: "subnet-2", CIDRs: []string{"10.2.0.0/24"}},
		{ProviderRef: api.LocalReference{Name: "gcp"}, Region: "us-central1", NetworkID: "vpc", SubnetID: "subnet-3", CIDRs: []string{"10.3.0.0/24"}},
	}}}
	raw, err := json.Marshal(network)
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	if err = json.Unmarshal(raw, &content); err != nil {
		t.Fatal(err)
	}
	if _, err := dc.Resource(schema.GroupVersionResource{Group: api.Group, Version: api.Version, Resource: "networkprofiles"}).Namespace(namespace).Create(ctx, &unstructured.Unstructured{Object: content}, metav1.CreateOptions{FieldValidation: "Strict"}); err != nil {
		t.Fatal(err)
	}
	class.Object["spec"].(map[string]any)["networkRef"] = map[string]any{"name": "private"}
	class, err = classes.Update(ctx, class, metav1.UpdateOptions{FieldValidation: "Strict"})
	if err != nil {
		t.Fatal(err)
	}
	if _, revisions, err := Read(ctx, KubernetesReader{Client: dc}, namespace, "build"); err != nil || len(revisions) != 7 {
		t.Fatalf("network profile snapshot failed: %v", err)
	}
	class.Object["spec"].(map[string]any)["retry"] = map[string]any{"enabled": true, "maxRetries": float64(1), "acknowledgeRepeatedEffects": true}
	if _, err := classes.Update(ctx, class, metav1.UpdateOptions{FieldValidation: "Strict"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Read(ctx, KubernetesReader{Client: dc}, namespace, "build"); err == nil {
		t.Fatal("unimplemented retry execution accepted")
	}
}
