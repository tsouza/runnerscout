package operator

import (
	"context"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/state"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
	"time"
)

func TestDurableAdmissionSurvivesOperatorRestart(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}
	s := &state.Kubernetes{Maps: k.CoreV1().ConfigMaps("test"), Owner: "test"}
	o := &Operator{Config: cfg, Client: k, Store: s}
	n, e := o.HandleDesiredRunnerCount(ctx, 3)
	if e != nil || n != 3 {
		t.Fatal(n, e)
	}
	o = &Operator{Config: cfg, Client: k, Store: s}
	n, e = o.HandleDesiredRunnerCount(ctx, 3)
	if e != nil || n != 3 {
		t.Fatal("lost pending capacity after restart", n, e)
	}
	_, f, e := o.loadFleet(ctx)
	if e != nil || len(f.Pending) != 3 || f.Admission.Admitted != 3 {
		t.Fatal(f, e)
	}
	for _, a := range f.Pending {
		if a.Phase != lifecycle.Pending || a.MaxAttempts != 3 || a.Deadline.Before(time.Now()) {
			t.Fatal(a)
		}
	}
}
