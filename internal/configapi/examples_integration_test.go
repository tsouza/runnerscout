//go:build integration

package configapi

import (
	"context"
	"strings"
	"testing"
	"time"

	api "github.com/tsouza/runnerscout/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

func verifyRealExamples(t *testing.T, ctx context.Context, kc kubernetes.Interface, dc dynamic.Interface, namespace string) {
	t.Helper()
	if _, err := kc.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := kc.CoreV1().Namespaces().Delete(cleanup, namespace, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Error(err)
		}
		if err := wait.PollUntilContextCancel(cleanup, time.Second, true, func(ctx context.Context) (bool, error) {
			_, err := kc.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}); err != nil {
			t.Error("example namespace cleanup", err)
		}
	}()
	reader := exampleReader(t)
	for key, original := range reader.objects {
		object := original.DeepCopy()
		object.SetNamespace(namespace)
		object.SetUID("")
		object.SetResourceVersion("")
		object.SetGeneration(0)
		resource, _, _ := strings.Cut(key, "/")
		if _, err := dc.Resource(schema.GroupVersionResource{Group: api.Group, Version: api.Version, Resource: resource}).Namespace(namespace).Create(ctx, object, metav1.CreateOptions{FieldValidation: "Strict"}); err != nil {
			t.Fatalf("example %s failed real schema validation: %v", key, err)
		}
	}
	snapshot, revisions, err := Read(ctx, KubernetesReader{Client: dc}, namespace, "build")
	if err != nil || len(revisions) != 7 || len(snapshot.Providers) != 3 || snapshot.Network == nil {
		t.Fatal("real examples did not resolve the complete graph", err)
	}
	roots := dc.Resource(schema.GroupVersionResource{Group: api.Group, Version: api.Version, Resource: "runnerscalesets"}).Namespace(namespace)
	valid, err := roots.Get(ctx, "build", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, crossNamespace := range []bool{false, true} {
		invalid := valid.DeepCopy()
		invalid.SetName("invalid-example")
		invalid.SetUID("")
		invalid.SetResourceVersion("")
		invalid.SetManagedFields(nil)
		invalid.SetCreationTimestamp(metav1.Time{})
		invalid.SetGeneration(0)
		if crossNamespace {
			_ = unstructured.SetNestedField(invalid.Object, "another-namespace", "spec", "runnerClassRef", "namespace")
		} else {
			_ = unstructured.SetNestedField(invalid.Object, int64(0), "spec", "maxRunners")
		}
		if _, err := roots.Create(ctx, invalid, metav1.CreateOptions{FieldValidation: "Strict"}); !apierrors.IsInvalid(err) && !apierrors.IsBadRequest(err) {
			t.Fatal("invalid or cross-namespace example did not produce a schema rejection", err)
		}
	}
}
