//go:build integration

package state

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"testing"
	"time"
)

func TestRealKubernetesPersistenceAndCAS(t *testing.T) {
	path := os.Getenv("RUNNERSCOUT_TEST_KUBECONFIG")
	if path == "" {
		t.Fatal("explicit isolated RUNNERSCOUT_TEST_KUBECONFIG required")
	}
	cfg, e := clientcmd.BuildConfigFromFlags("", path)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Timeout = 10 * time.Second
	client, e := kubernetes.NewForConfig(cfg)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ns := "runnerscout-test-" + uuid.NewString()
	if _, e = client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); e != nil {
		t.Fatal(e)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if e := client.CoreV1().Namespaces().Delete(cleanup, ns, metav1.DeleteOptions{}); e != nil {
			t.Error(e)
			return
		}
		for {
			_, e := client.CoreV1().Namespaces().Get(cleanup, ns, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				return
			}
			select {
			case <-cleanup.Done():
				t.Error("namespace cleanup not confirmed")
				return
			case <-time.After(time.Second):
			}
		}
	}()
	s := Kubernetes{Maps: client.CoreV1().ConfigMaps(ns), Owner: "integration"}
	a, e := s.Save(ctx, lifecycle.Allocation{ID: "rs-test", Phase: lifecycle.Pending}, "")
	if e != nil {
		t.Fatal(e)
	}
	client2, e := kubernetes.NewForConfig(cfg)
	if e != nil {
		t.Fatal(e)
	}
	s2 := Kubernetes{Maps: client2.CoreV1().ConfigMaps(ns), Owner: "integration"}
	b, e := s2.Load(ctx, a.ID)
	if e != nil || b.Revision == "" {
		t.Fatal(b, e)
	}
	a.Phase = lifecycle.Creating
	a, e = s.Save(ctx, a, a.Revision)
	if e != nil {
		t.Fatal(e)
	}
	b.Phase = lifecycle.Running
	if _, e = s2.Save(ctx, b, b.Revision); !errors.Is(e, lifecycle.ErrConflict) {
		t.Fatalf("stale revision was not rejected: %v", e)
	}
	got, e := s2.Load(ctx, a.ID)
	if e != nil || got.Phase != lifecycle.Creating {
		t.Fatal(got, e)
	}
}
