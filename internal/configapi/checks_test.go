package configapi

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

func assertReadOnly(t *testing.T, actions []kt.Action) {
	t.Helper()
	for _, action := range actions {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatalf("read-only check performed %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
}

func TestCRDCheckValidatesSecretsWithoutStartingWorkers(t *testing.T) {
	f := newRuntimeFixture(t)
	client := f.r.Client.(*fake.Clientset)
	client.ClearActions()
	if err := f.r.CheckConfiguration(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.workers) != 0 {
		t.Fatal("read-only check started a session")
	}
	assertReadOnly(t, client.Actions())
	assertReadOnly(t, f.r.Dynamic.(*dynamicfake.FakeDynamicClient).Actions())
	if err := client.CoreV1().Secrets("test").Delete(context.Background(), "github", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := f.r.CheckConfiguration(context.Background()); err == nil {
		t.Fatal("missing credentials passed configuration check")
	}
}

func TestUninstallCheckRequiresRootAbsenceAndIsReadOnly(t *testing.T) {
	f := newRuntimeFixture(t)
	ctx := context.Background()
	if err := f.r.CheckUninstall(ctx); err == nil {
		t.Fatal("uninstall accepted while root exists")
	}
	snapshot, _, err := Read(ctx, KubernetesReader{Client: f.r.Dynamic}, "test", "build")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.r.checkpoints().Save(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := f.r.roots().Delete(ctx, "build", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	client := f.r.Client.(*fake.Clientset)
	client.ClearActions()
	f.r.Dynamic.(*dynamicfake.FakeDynamicClient).ClearActions()
	if err := f.r.CheckUninstall(ctx); err != nil {
		t.Fatal("completed empty fleet was rejected", err)
	}
	assertReadOnly(t, client.Actions())
	assertReadOnly(t, f.r.Dynamic.(*dynamicfake.FakeDynamicClient).Actions())
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "secrets" {
			t.Fatal("uninstall check read credentials")
		}
	}
	if len(f.workers) != 0 {
		t.Fatal("uninstall check started a cloud worker")
	}
}

func TestUninstallCheckRefusesMissingCheckpointOrLifetimeRecords(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing checkpoint", true: "missing allocation"}[checkpoint], func(t *testing.T) {
			f := newRuntimeFixture(t)
			ctx := context.Background()
			if checkpoint {
				snapshot, _, err := Read(ctx, KubernetesReader{Client: f.r.Dynamic}, "test", "build")
				if err != nil {
					t.Fatal(err)
				}
				if err = f.r.checkpoints().Save(ctx, snapshot); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.r.roots().Delete(ctx, "build", metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			_, err := f.r.Client.CoreV1().ConfigMaps("test").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "build-fleet", Namespace: "test", Labels: map[string]string{"runnerscout/owner": "build"}}, Data: map[string]string{"fleet": `{"created":{"rs-unresolved":"2026-01-01T00:00:00Z"}}`}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			client := f.r.Client.(*fake.Clientset)
			client.ClearActions()
			if err := f.r.CheckUninstall(ctx); err == nil {
				t.Fatal("lost durable cleanup records permitted uninstall")
			}
			assertReadOnly(t, client.Actions())
		})
	}
}

func TestUninstallCheckDetectsConcurrentRootRecreation(t *testing.T) {
	f := newRuntimeFixture(t)
	ctx := context.Background()
	root, err := f.r.roots().Get(ctx, "build", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.r.roots().Delete(ctx, "build", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	reads := 0
	f.r.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "runnerscalesets", func(action kt.Action) (bool, runtime.Object, error) {
		reads++
		if reads == 2 {
			root.SetUID("replacement")
			return true, root, nil
		}
		return false, nil, nil
	})
	if err := f.r.CheckUninstall(ctx); err == nil {
		t.Fatal("concurrent root recreation passed uninstall check")
	}
}
