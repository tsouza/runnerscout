package operator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

// fakeDeregistrar is a test-only lifecycle.RunnerDeregistrar: fail, when set,
// makes every call return an error regardless of id, so pruning's own retry
// behavior on a failed verification can be exercised directly.
type fakeDeregistrar struct {
	calls []string
	fail  bool
}

func (d *fakeDeregistrar) DeregisterRunner(_ context.Context, id string) error {
	d.calls = append(d.calls, id)
	if d.fail {
		return errors.New("deregistration verification failed")
	}
	return nil
}

// Deleted/TimedOut records must not accumulate forever - see
// pruneTerminalAllocations's own doc comment for why a Deleted record
// additionally requires f.Released before it is safe to remove, and why
// every candidate is re-verified against GitHub right before deletion
// rather than trusting a flag some earlier Step call may have set: that
// re-verification is what makes pruning correct uniformly for every
// record, including ones that existed before this logic shipped - there is
// deliberately no special-casing for "old" records in the fixtures below,
// because the design needs none.
func TestTickPrunesTerminalAllocationsPastRetention(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}
	o := New(cfg, k, nil)
	d := &fakeDeregistrar{}
	o.Controller.Runners = d

	old := time.Now().Add(-48 * time.Hour)
	const (
		timedOutID          = "rs-timedout"
		deletedReleasedID   = "rs-deleted-released"
		deletedUnreleasedID = "rs-deleted-unreleased"
		freshTimedOutID     = "rs-fresh-timedout"
	)
	// Retire: true on every fixture skips Tick's own retire-forcing Save
	// (o.Config.MaxLifetimeSeconds has long since passed relative to `old`)
	// - irrelevant to what this test exercises, and this package's fake
	// clientset does not simulate real resourceVersion bumps the way a real
	// cluster does (see docs/operations.md's own note that only
	// `make integration` exercises real CAS), so a redundant Save here would
	// spuriously conflict.
	for _, a := range []lifecycle.Allocation{
		{ID: timedOutID, Phase: lifecycle.TimedOut, Deadline: old, MaxAttempts: 3, Retire: true, TerminalAt: old},
		{ID: deletedReleasedID, Phase: lifecycle.Deleted, Deadline: old, MaxAttempts: 3, Retire: true, TerminalAt: old},
		{ID: deletedUnreleasedID, Phase: lifecycle.Deleted, Deadline: old, MaxAttempts: 3, Retire: true, TerminalAt: old},
		{ID: freshTimedOutID, Phase: lifecycle.TimedOut, Deadline: old, MaxAttempts: 3, Retire: true, TerminalAt: time.Now()},
	} {
		if _, e := o.Store.Save(ctx, a, ""); e != nil {
			t.Fatal(e)
		}
	}

	cm, f, e := o.loadFleet(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{timedOutID, deletedReleasedID, deletedUnreleasedID, freshTimedOutID} {
		f.Created[id] = time.Now()
	}
	f.Released[deletedReleasedID] = true
	if e := o.saveFleet(ctx, cm, f); e != nil {
		t.Fatal(e)
	}

	if e := o.Tick(ctx); e != nil {
		t.Fatal(e)
	}

	if _, e := o.Store.Load(ctx, timedOutID); e == nil {
		t.Fatal("TimedOut record past retention must be pruned")
	}
	if _, e := o.Store.Load(ctx, deletedReleasedID); e == nil {
		t.Fatal("Deleted record already released and past retention must be pruned")
	}
	if _, e := o.Store.Load(ctx, deletedUnreleasedID); e != nil {
		t.Fatal("a Deleted record must never be pruned before admission accounting has observed it via Released", e)
	}
	if _, e := o.Store.Load(ctx, freshTimedOutID); e != nil {
		t.Fatal("a terminal record within retention must not be pruned", e)
	}
	for _, id := range []string{timedOutID, deletedReleasedID} {
		found := false
		for _, called := range d.calls {
			found = found || called == id
		}
		if !found {
			t.Fatal("pruning must re-verify the registration before deleting", id, d.calls)
		}
	}
}

// A failed verification must retain the record for a later retry, never
// delete it on an unconfirmed answer - the same discipline Step itself
// already applies to a failed deregister call before persisting a phase
// transition (see lifecycle.Controller's own doc comment on deregister).
func TestTickRetainsRecordWhenDeregistrationVerificationFails(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}
	o := New(cfg, k, nil)
	d := &fakeDeregistrar{fail: true}
	o.Controller.Runners = d

	old := time.Now().Add(-48 * time.Hour)
	const id = "rs-verification-fails"
	a := lifecycle.Allocation{ID: id, Phase: lifecycle.TimedOut, Deadline: old, MaxAttempts: 3, Retire: true, TerminalAt: old}
	if _, e := o.Store.Save(ctx, a, ""); e != nil {
		t.Fatal(e)
	}
	cm, f, e := o.loadFleet(ctx)
	if e != nil {
		t.Fatal(e)
	}
	f.Created[id] = time.Now()
	if e := o.saveFleet(ctx, cm, f); e != nil {
		t.Fatal(e)
	}

	if e := o.Tick(ctx); e == nil {
		t.Fatal("a failed verification must surface as an error, not be silently swallowed")
	}
	if _, e := o.Store.Load(ctx, id); e != nil {
		t.Fatal("a record must never be deleted on an unconfirmed deregistration verification", e)
	}
	if len(d.calls) != 1 {
		t.Fatal("expected exactly one verification attempt", d.calls)
	}
}

// Runners entirely unconfigured (should never happen in a real deployment -
// operator.New always wires a runnerDeregistrar - but this test documents
// the deliberate fail-safe for it) must retain every candidate rather than
// prune without ever having asked GitHub anything.
func TestTickNeverPrunesWithoutRunnersConfigured(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}
	o := New(cfg, k, nil)
	o.Controller.Runners = nil

	old := time.Now().Add(-48 * time.Hour)
	const id = "rs-no-runners"
	a := lifecycle.Allocation{ID: id, Phase: lifecycle.TimedOut, Deadline: old, MaxAttempts: 3, Retire: true, TerminalAt: old}
	if _, e := o.Store.Save(ctx, a, ""); e != nil {
		t.Fatal(e)
	}
	cm, f, e := o.loadFleet(ctx)
	if e != nil {
		t.Fatal(e)
	}
	f.Created[id] = time.Now()
	if e := o.saveFleet(ctx, cm, f); e != nil {
		t.Fatal(e)
	}

	if e := o.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := o.Store.Load(ctx, id); e != nil {
		t.Fatal("a record must never be pruned when Runners is unconfigured", e)
	}
}

// Issue #170: fleet.Created is never removed, so once a record is pruned
// its ID remains in Created with no matching Store entry - Drained must
// treat that as confirmed-resolved-and-cleaned-up (via fleet.Pruned), not
// as unresolved, or a deployment that has ever pruned a single record could
// never again confirm drain/uninstall.
func TestDrainedRemainsTrueAfterPruning(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}
	o := New(cfg, k, nil)
	o.Controller.Runners = &fakeDeregistrar{}

	old := time.Now().Add(-48 * time.Hour)
	const id = "rs-prune-then-drain"
	a := lifecycle.Allocation{ID: id, Phase: lifecycle.TimedOut, Deadline: old, MaxAttempts: 3, Retire: true, TerminalAt: old}
	if _, e := o.Store.Save(ctx, a, ""); e != nil {
		t.Fatal(e)
	}
	cm, f, e := o.loadFleet(ctx)
	if e != nil {
		t.Fatal(e)
	}
	f.Created[id] = time.Now()
	if e := o.saveFleet(ctx, cm, f); e != nil {
		t.Fatal(e)
	}

	if e := o.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := o.Store.Load(ctx, id); e == nil {
		t.Fatal("expected the record to have been pruned as a precondition of this test")
	}

	done, e := o.Drained(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if !done {
		t.Fatal("Drained must remain true for an allocation whose record was pruned after confirmed cleanup")
	}
}

// pruneTerminalAllocations must persist f.Pruned before deleting any Store
// record, not after: deleting first and saving f.Pruned once at the end
// would mean a single failed fleet save after some deletes had already
// succeeded permanently strands those records' IDs in fleet.Created with no
// way to ever mark them Pruned again (issue #170, reopened). Injects a
// failure into exactly the fleet ConfigMap's own update call (not the
// allocation's own Store record) to isolate this ordering.
func TestPruneSurvivesAFailedFleetSave(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}
	o := New(cfg, k, nil)
	o.Controller.Runners = &fakeDeregistrar{}

	old := time.Now().Add(-48 * time.Hour)
	const id = "rs-fleet-save-fails"
	a := lifecycle.Allocation{ID: id, Phase: lifecycle.TimedOut, Deadline: old, MaxAttempts: 3, Retire: true, TerminalAt: old}
	if _, e := o.Store.Save(ctx, a, ""); e != nil {
		t.Fatal(e)
	}
	cm, f, e := o.loadFleet(ctx)
	if e != nil {
		t.Fatal(e)
	}
	f.Created[id] = time.Now()
	if e := o.saveFleet(ctx, cm, f); e != nil {
		t.Fatal(e)
	}

	// The second update to the fleet ConfigMap in a Tick call is
	// pruneTerminalAllocations's own save (the first is Tick's own
	// unconditional f.Pending-migration save at the top of the method).
	updates := 0
	k.PrependReactor("update", "configmaps", func(action kt.Action) (bool, runtime.Object, error) {
		if action.GetResource().Resource != "configmaps" {
			return false, nil, nil
		}
		updates++
		if updates == 2 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, cfg.Name+"-fleet", errors.New("injected conflict"))
		}
		return false, nil, nil
	})

	if e := o.Tick(ctx); e == nil {
		t.Fatal("expected the injected fleet save failure to surface as an error")
	}
	if _, e := o.Store.Load(ctx, id); e != nil {
		t.Fatal("record must survive intact when pruning's own fleet save fails - nothing may be deleted on an unconfirmed save", e)
	}

	// A later Tick, once the fleet save succeeds, must prune cleanly - the
	// failed attempt above cost nothing beyond retrying the (idempotent)
	// deregistration verification.
	if e := o.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := o.Store.Load(ctx, id); e == nil {
		t.Fatal("expected the record to be pruned once the fleet save succeeds")
	}
}
