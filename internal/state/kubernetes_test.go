package state

import (
	"context"
	"encoding/json"
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
func TestDeleteRemovesRecordAndNoOpsWhenAlreadyGone(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	s := Kubernetes{Maps: client.CoreV1().ConfigMaps("test"), Owner: "owner"}
	a := lifecycle.Allocation{ID: "rs-test", Phase: lifecycle.TimedOut}
	if _, e := s.Save(ctx, a, ""); e != nil {
		t.Fatal(e)
	}
	if e := s.Delete(ctx, a.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Load(ctx, a.ID); e == nil {
		t.Fatal("record survived Delete")
	}
	// Deleting an already-gone record is a no-op, not an error - a retried
	// prune pass (e.g. after a partial fleet-write failure) must not fail.
	if e := s.Delete(ctx, a.ID); e != nil {
		t.Fatal("deleting an already-absent record must no-op", e)
	}
}
func TestDeleteRejectsForeignState(t *testing.T) {
	c := fake.NewClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "rs-test", Namespace: "test", Labels: map[string]string{"runnerscout/owner": "foreign"}}, Data: map[string]string{"allocation": `{"id":"rs-test"}`}})
	s := Kubernetes{Maps: c.CoreV1().ConfigMaps("test"), Owner: "owner"}
	if e := s.Delete(context.Background(), "rs-test"); e == nil {
		t.Fatal("deleted foreign allocation")
	}
	if _, e := c.CoreV1().ConfigMaps("test").Get(context.Background(), "rs-test", metav1.GetOptions{}); e != nil {
		t.Fatal("foreign record must survive a rejected delete", e)
	}
}
func TestForeignStateRejected(t *testing.T) {
	c := fake.NewClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "rs-test", Namespace: "test", Labels: map[string]string{"runnerscout/owner": "foreign"}}, Data: map[string]string{"allocation": `{"id":"rs-test"}`}})
	s := Kubernetes{Maps: c.CoreV1().ConfigMaps("test"), Owner: "owner"}
	if _, e := s.Load(context.Background(), "rs-test"); e == nil {
		t.Fatal("read foreign allocation")
	}
}

func TestDependencyStateCannotBeDowngradedOrDiscarded(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	s := Kubernetes{Maps: client.CoreV1().ConfigMaps("test"), Owner: "owner"}
	a := lifecycle.Allocation{ID: "rs-test", Phase: lifecycle.Deleting, Resources: []lifecycle.ResourceReference{{Kind: "aws-volume", ID: "volume-1"}}}
	if _, err := s.Save(ctx, a, ""); err != nil {
		t.Fatal(err)
	}
	cm, err := s.Maps.Get(ctx, a.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cm.ResourceVersion = "1"
	if _, err := s.Maps.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	// A pre-migration reader must fail decoding, rather than silently discard
	// the fields it does not know before saving a later allocation transition.
	var legacy struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(cm.Data["allocation"]), &legacy) == nil {
		t.Fatal("old reader can silently drop dependency records")
	}
	restarted := Kubernetes{Maps: client.CoreV1().ConfigMaps("test"), Owner: "owner"}
	loaded, err := restarted.Load(ctx, a.ID)
	if err != nil || len(loaded.Resources) != 1 {
		t.Fatal("restart lost dependencies", err)
	}
	all, err := restarted.List(ctx)
	if err != nil || len(all) != 1 || len(all[0].Resources) != 1 {
		t.Fatal("list lost dependencies", err)
	}
	loaded.Resources = nil
	if _, err := restarted.Save(ctx, loaded, loaded.Revision); err == nil {
		t.Fatal("writer discarded a recorded cleanup dependency")
	}
	retained, err := restarted.Load(ctx, a.ID)
	if err != nil || len(retained.Resources) != 1 {
		t.Fatal("failed downgrade changed durable state", err)
	}
}

func TestUnknownAllocationFieldsAreNotSilentlyLost(t *testing.T) {
	ctx := context.Background()
	for _, data := range []map[string]string{
		{"allocation": `{"id":"rs-test","futureDependencies":["vol-owned"]}`},
		{"allocation": `{"id":"rs-test","resources":[{"kind":"aws-volume","id":"vol-owned"}]}`},
		{"allocation": `{"id":"rs-test"}`, "allocation-v2": `{"id":"rs-test"}`},
	} {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "rs-test", Namespace: "test", ResourceVersion: "1", Labels: map[string]string{"runnerscout/owner": "owner", "runnerscout/kind": "allocation"}}, Data: data}
		client := fake.NewClientset(cm)
		s := Kubernetes{Maps: client.CoreV1().ConfigMaps("test"), Owner: "owner"}
		if _, err := s.Load(ctx, "rs-test"); err == nil {
			t.Fatal("unsupported state was read")
		}
		if _, err := s.List(ctx); err == nil {
			t.Fatal("unsupported state was listed")
		}
		if _, err := s.Save(ctx, lifecycle.Allocation{ID: "rs-test"}, "1"); err == nil {
			t.Fatal("unsupported state was overwritten")
		}
	}
}
