package state

import (
	"context"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestPersistenceAcrossStoreInstances(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	s := Kubernetes{Maps: client.CoreV1().ConfigMaps("test"), Owner: "owner"}
	a := lifecycle.Allocation{ID: "rs-test", Phase: lifecycle.Creating, Attempts: 1}
	if _, e := s.Save(ctx, a, ""); e != nil {
		t.Fatal(e)
	}
	s2 := Kubernetes{Maps: client.CoreV1().ConfigMaps("test"), Owner: "owner"}
	got, e := s2.Load(ctx, a.ID)
	if e != nil || got.Phase != lifecycle.Creating || got.Attempts != 1 {
		t.Fatal(got, e)
	}
}
func TestForeignStateRejected(t *testing.T) {
	c := fake.NewClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "rs-test", Namespace: "test", Labels: map[string]string{"runnerscout/owner": "foreign"}}, Data: map[string]string{"allocation": `{"id":"rs-test"}`}})
	s := Kubernetes{Maps: c.CoreV1().ConfigMaps("test"), Owner: "owner"}
	if _, e := s.Load(context.Background(), "rs-test"); e == nil {
		t.Fatal("read foreign allocation")
	}
}
