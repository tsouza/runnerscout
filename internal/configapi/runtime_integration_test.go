//go:build integration

package configapi

import (
	"context"
	"errors"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Cloud workers are explicit fixtures here. The API server, Secret revisions,
// checkpoints, status updates and finalizer deletion lifecycle are real.
func verifyRealRuntimeLifecycle(t *testing.T, ctx context.Context, kc kubernetes.Interface, dc dynamic.Interface, namespace string) {
	t.Helper()
	r := &Runtime{Namespace: namespace, Name: "runtime-build", Client: kc, Dynamic: dc}
	root, err := r.roots().Get(ctx, "build", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	root.SetName(r.Name)
	root.SetUID("")
	root.SetResourceVersion("")
	root.SetGeneration(0)
	root.SetCreationTimestamp(metav1.Time{})
	root.SetManagedFields(nil)
	root.SetFinalizers([]string{"example.com/test-retain"})
	delete(root.Object, "status")
	root, err = r.roots().Create(ctx, root, metav1.CreateOptions{FieldValidation: "Strict"})
	if err != nil {
		t.Fatal(err)
	}
	uid := root.GetUID()
	defer func() {
		if err := r.stop(); err != nil {
			t.Error(err)
		}
		// Only this task-owned fixture has mocked cloud workers. Ensure a failed
		// assertion cannot strand the disposable qualification namespace.
		current, err := r.roots().Get(context.Background(), r.Name, metav1.GetOptions{})
		if err == nil && current.GetUID() == uid {
			current.SetFinalizers(nil)
			if _, err := r.roots().Update(context.Background(), current, metav1.UpdateOptions{}); err != nil {
				t.Error(err)
			}
		}
	}()
	var workers []*runtimeWorker
	var cleanups int
	r.Factory = func(resolved Resolved, _ Credentials, mode WorkerMode, ready func(bool)) (Worker, func() error, error) {
		w := &runtimeWorker{started: make(chan struct{}), finish: make(chan struct{}), stopped: make(chan struct{})}
		w.PauseAdmissions(resolved.Suspend)
		w.beforeStart = func() error {
			checkpoint, err := r.checkpoints().Read(ctx)
			if err != nil {
				return err
			}
			current, err := r.roots().Get(ctx, r.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if checkpoint.ScaleSet.UID != uid || !slices.Contains(current.GetFinalizers(), Finalizer) {
				return errors.New("cloud worker started without durable cleanup protection")
			}
			ready(true)
			return nil
		}
		workers = append(workers, w)
		return w, func() error { cleanups++; return nil }, nil
	}
	reconcileRuntime(t, r)
	awaitRuntime(t, workers[0].started)
	reconcileRuntime(t, r)
	reconcileRuntime(t, r)
	if len(workers) != 1 || runtimeReason(t, r) != "Reconciled" {
		t.Fatal("real status writes restarted the worker")
	}
	secret, err := kc.CoreV1().Secrets(namespace).Get(ctx, "github", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret.Data["token"] = []byte("runtime-rotated-fixture-token")
	if _, err := kc.CoreV1().Secrets(namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	reconcileRuntime(t, r)
	if len(workers) != 2 || cleanups != 1 {
		t.Fatal("real Secret rotation did not replace the worker")
	}
	awaitRuntime(t, workers[1].started)
	if err := r.roots().Delete(ctx, r.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := kc.CoreV1().Secrets(namespace).Delete(ctx, "github", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	reconcileRuntime(t, r)
	if len(workers) != 3 {
		t.Fatal("real deletion failed without GitHub credentials")
	}
	w := workers[2]
	awaitRuntime(t, w.started)
	if !w.draining.Load() {
		t.Fatal("deletion did not drain")
	}
	close(w.finish)
	awaitRuntime(t, r.worker.done)
	reconcileRuntime(t, r)
	current, err := r.roots().Get(ctx, r.Name, metav1.GetOptions{})
	if err != nil || !slices.Contains(current.GetFinalizers(), Finalizer) {
		t.Fatal("unconfirmed cloud absence removed the finalizer", err)
	}
	w.drained.Store(true)
	reconcileRuntime(t, r)
	current, err = r.roots().Get(ctx, r.Name, metav1.GetOptions{})
	if err != nil || !slices.Equal(current.GetFinalizers(), []string{"example.com/test-retain"}) {
		t.Fatal("real finalizer completion affected another owner", err)
	}
	checkpoint, err := r.checkpoints().Read(ctx)
	if err != nil || checkpoint.ScaleSet.UID != uid {
		t.Fatal("cleanup lost accepted ownership evidence", err)
	}
}
