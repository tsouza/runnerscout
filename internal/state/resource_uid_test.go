package state

import (
	"context"
	"testing"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDurableResourceGenerationSurvivesRestartAndCannotChange(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	s := Kubernetes{Maps: client.CoreV1().ConfigMaps("test"), Owner: "owner"}
	a := lifecycle.Allocation{ID: "rs-test", Resources: []lifecycle.ResourceReference{{Kind: "azure-disk", ID: "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/disks/rs-test-os", UID: "11111111-1111-4111-8111-111111111111"}}}
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
	restarted := Kubernetes{Maps: client.CoreV1().ConfigMaps("test"), Owner: "owner"}
	for _, changed := range []string{"", "22222222-2222-4222-8222-222222222222"} {
		loaded, err := restarted.Load(ctx, a.ID)
		if err != nil || len(loaded.Resources) != 1 || loaded.Resources[0] != a.Resources[0] {
			t.Fatal("restart lost immutable identity", loaded, err)
		}
		loaded.Resources[0].UID = changed
		if _, err := restarted.Save(ctx, loaded, loaded.Revision); err == nil {
			t.Fatal("generation change reached durable state", changed)
		}
	}
	listed, err := restarted.List(ctx)
	if err != nil || len(listed) != 1 || len(listed[0].Resources) != 1 || listed[0].Resources[0] != a.Resources[0] {
		t.Fatal("rejected generation change mutated state", listed, err)
	}
}
