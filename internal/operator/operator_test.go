package operator

import (
	"context"
	"github.com/actions/scaleset"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/state"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
	"time"
)

func TestDurableAdmissionSurvivesOperatorRestart(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}
	cfg.Requirements = placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: []string{"p"}, Regions: []string{"r"}, Policy: "lowest-price"}
	cfg.Catalog = placement.Catalog{Complete: map[string]bool{"p": true}, Offerings: []placement.Offering{{ID: "pool", Provider: "p", Region: "r", Zone: "z", Machine: "m", Image: "i", CPU: 1, MemoryMiB: 1, Architecture: "amd64", PriceMicros: 1, Currency: "USD", Spot: true, ObservedAt: time.Now()}}}
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

func TestProviderBindingCannotChangeUnderExistingFleet(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	o := &Operator{Config: Config{Name: "test", Namespace: "test"}, Client: k}
	cm, f, e := o.loadFleet(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = o.saveFleet(ctx, cm, f); e != nil {
		t.Fatal(e)
	}
	o.Config.ScaleSetID = 42
	if _, _, e = o.loadFleet(ctx); e == nil {
		t.Fatal("configuration drift silently adopted")
	}
}

func TestLimitUpgradeMigratesLegacyBindingButPreservesIdentity(t *testing.T) {
	ctx := context.Background()
	o := &Operator{Config: Config{Name: "test", Namespace: "test", MaxRunners: 1}, Client: fake.NewClientset()}
	cm, f, e := o.loadFleet(ctx)
	if e != nil {
		t.Fatal(e)
	}
	f.Binding = o.bindingWithLimit(1)
	f.BindingVersion = 0
	if e = o.saveFleet(ctx, cm, f); e != nil {
		t.Fatal(e)
	}
	o.Config.MaxRunners = 2
	cm, f, e = o.loadFleet(ctx)
	if e != nil || f.BindingVersion != 2 || f.Binding != o.binding() {
		t.Fatal(f, e)
	}
	if e = o.saveFleet(ctx, cm, f); e != nil {
		t.Fatal(e)
	}
	o.Config.MaxRunners = 1
	if _, _, e = o.loadFleet(ctx); e != nil {
		t.Fatal("limit rollback rejected", e)
	}
	o.Config.ScaleSetID = 99
	if _, _, e = o.loadFleet(ctx); e == nil {
		t.Fatal("identity drift accepted")
	}
}

// bumpResourceVersion works around the fake clientset never assigning a
// resourceVersion on its own, which Kubernetes.Save's CAS otherwise relies on
// to distinguish a create from an update (kubernetes_test.go uses the same
// idiom for the same reason).
func bumpResourceVersion(t *testing.T, ctx context.Context, s *state.Kubernetes, id, version string) {
	t.Helper()
	cm, e := s.Maps.Get(ctx, id, metav1.GetOptions{})
	if e != nil {
		t.Fatal(e)
	}
	cm.ResourceVersion = version
	if _, e := s.Maps.Update(ctx, cm, metav1.UpdateOptions{}); e != nil {
		t.Fatal(e)
	}
}
func TestJobStartCapturesRunIdentityOnce(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	s := &state.Kubernetes{Maps: k.CoreV1().ConfigMaps("test"), Owner: "test"}
	o := &Operator{Config: Config{Name: "test", Namespace: "test"}, Client: k, Store: s}
	seed := lifecycle.Allocation{ID: "rs-test", Phase: lifecycle.Creating, Deadline: time.Now().Add(time.Minute), MaxAttempts: 3}
	if _, e := s.Save(ctx, seed, ""); e != nil {
		t.Fatal(e)
	}
	bumpResourceVersion(t, ctx, s, "rs-test", "1")
	first := &scaleset.JobStarted{RunnerName: "rs-test", JobMessageBase: scaleset.JobMessageBase{WorkflowRunID: 99, OwnerName: "acme", RepositoryName: "widgets", JobID: "opaque-guid"}}
	if e := o.HandleJobStarted(ctx, first); e != nil {
		t.Fatal(e)
	}
	got, e := s.Load(ctx, "rs-test")
	if e != nil || !got.Ready || got.RunID != 99 || got.Owner != "acme" || got.Repo != "widgets" || got.ScaleSetJobID != "opaque-guid" {
		t.Fatal("run identity not captured", got, e)
	}
	bumpResourceVersion(t, ctx, s, "rs-test", "2")
	// A second job-started message for the same runner must never overwrite an
	// already-captured identity - each allocation runs exactly one job.
	second := &scaleset.JobStarted{RunnerName: "rs-test", JobMessageBase: scaleset.JobMessageBase{WorkflowRunID: 12345, OwnerName: "other", RepositoryName: "other-repo", JobID: "different"}}
	if e := o.HandleJobStarted(ctx, second); e != nil {
		t.Fatal(e)
	}
	got, e = s.Load(ctx, "rs-test")
	if e != nil || got.RunID != 99 || got.Owner != "acme" || got.Repo != "widgets" || got.ScaleSetJobID != "opaque-guid" {
		t.Fatal("run identity was overwritten by a later message", got, e)
	}
}
