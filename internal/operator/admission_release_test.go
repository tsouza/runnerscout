package operator

import (
	"context"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/state"
	"k8s.io/client-go/kubernetes/fake"
)

// A spot interruption, a MaxLifetimeSeconds expiry, or a drain-before-pickup
// all reach lifecycle.Deleted (cleanup confirmed - the cloud resource is
// provably gone) without ever completing a GitHub job, so Allocation.Completed
// stays false. The admission slot must still be released: Deleted already
// means "safe to release," independent of whether a job ever ran on it.
func TestAdmissionReleasesOnDeletedWithoutCompletedJob(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}
	cfg.Requirements = placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: []string{"p"}, Regions: []string{"r"}, Policy: "lowest-price"}
	cfg.Catalog = placement.Catalog{Complete: map[string]bool{"p": true}, Offerings: []placement.Offering{{ID: "pool", Provider: "p", Region: "r", Zone: "z", Machine: "m", Image: "i", CPU: 1, MemoryMiB: 1, Architecture: "amd64", PriceMicros: 1, Currency: "USD", Spot: true, ObservedAt: time.Now()}}}
	s := &state.Kubernetes{Maps: k.CoreV1().ConfigMaps("test"), Owner: "test"}
	o := &Operator{Config: cfg, Client: k, Store: s}

	n, e := o.HandleDesiredRunnerCount(ctx, 1)
	if e != nil || n != 1 {
		t.Fatal("expected one admission", n, e)
	}
	_, f, e := o.loadFleet(ctx)
	if e != nil || len(f.Pending) != 1 || f.Admission.Admitted != 1 {
		t.Fatal("expected exactly one pending, one admitted", f, e)
	}
	var id string
	for pendingID := range f.Pending {
		id = pendingID
	}

	// Simulate the allocation being interrupted and confirmed cleaned up
	// before ever completing a job: Phase reaches Deleted, Completed stays
	// false - exactly the spot-interruption/expiry/drain-before-pickup case.
	a := lifecycle.Allocation{ID: id, Phase: lifecycle.Deleted, Condition: "ResourceAbsentConfirmedInterruption"}
	if _, e := s.Save(ctx, a, ""); e != nil {
		t.Fatal(e)
	}

	o = &Operator{Config: cfg, Client: k, Store: s}
	if _, e := o.HandleDesiredRunnerCount(ctx, 0); e != nil {
		t.Fatal(e)
	}
	_, f, e = o.loadFleet(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if f.Admission.Admitted != 0 {
		t.Fatal("admission slot leaked for a Deleted allocation that never completed a job", f.Admission.Admitted)
	}
	if !f.Released[id] {
		t.Fatal("allocation not marked released", f.Released)
	}
}

// Issue #174: a TimedOut allocation is just as provably free of cloud
// resources as a Deleted one (lifecycle.Step's own Pending-phase invariant
// - "pending allocation retains cloud resources" is a hard error otherwise -
// guarantees it), so it must release its admission slot the same way, not
// only via the separate zero-demand Cohort reset (admission.State.Reconcile),
// which under sustained real demand may never fire at all. Confirmed in
// production: a batch of TimedOut allocations under a real, persistent PR
// backlog permanently consumed their slots until a human deleted the fleet
// ConfigMap by hand.
func TestAdmissionReleasesOnTimedOutUnderSustainedDemand(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}
	cfg.Requirements = placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: []string{"p"}, Regions: []string{"r"}, Policy: "lowest-price"}
	cfg.Catalog = placement.Catalog{Complete: map[string]bool{"p": true}, Offerings: []placement.Offering{{ID: "pool", Provider: "p", Region: "r", Zone: "z", Machine: "m", Image: "i", CPU: 1, MemoryMiB: 1, Architecture: "amd64", PriceMicros: 1, Currency: "USD", Spot: true, ObservedAt: time.Now()}}}
	s := &state.Kubernetes{Maps: k.CoreV1().ConfigMaps("test"), Owner: "test"}
	o := &Operator{Config: cfg, Client: k, Store: s}

	n, e := o.HandleDesiredRunnerCount(ctx, 1)
	if e != nil || n != 1 {
		t.Fatal("expected one admission", n, e)
	}
	_, f, e := o.loadFleet(ctx)
	if e != nil || len(f.Pending) != 1 || f.Admission.Admitted != 1 {
		t.Fatal("expected exactly one pending, one admitted", f, e)
	}
	var id string
	for pendingID := range f.Pending {
		id = pendingID
	}

	a := lifecycle.Allocation{ID: id, Phase: lifecycle.TimedOut, Condition: "LocalProvisioningTimeout"}
	if _, e := s.Save(ctx, a, ""); e != nil {
		t.Fatal(e)
	}

	// Demand stays high, matching the real production backlog - the
	// separate zero-demand Cohort reset can never fire here. The slot must
	// still release, and - because demand remains real - a fresh admission
	// must actually be granted to replace it, not just bookkeeping catching
	// up with no observable effect.
	o = &Operator{Config: cfg, Client: k, Store: s}
	n, e = o.HandleDesiredRunnerCount(ctx, 16)
	if e != nil {
		t.Fatal(e)
	}
	if n == 0 {
		t.Fatal("expected a fresh admission once the TimedOut slot released under continued demand", n)
	}
	_, f, e = o.loadFleet(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if !f.Released[id] {
		t.Fatal("allocation not marked released", f.Released)
	}
}
