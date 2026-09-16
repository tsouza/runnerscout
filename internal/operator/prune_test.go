package operator

import (
	"context"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"k8s.io/client-go/kubernetes/fake"
)

// Deleted/TimedOut records must not accumulate forever - see
// pruneTerminalAllocations's own doc comment for why a Deleted record additionally
// requires f.Released before it is safe to remove.
func TestTickPrunesTerminalAllocationsPastRetention(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}
	o := New(cfg, k, nil)

	old := time.Now().Add(-48 * time.Hour)
	const (
		timedOutID            = "rs-timedout"
		deletedReleasedID     = "rs-deleted-released"
		deletedUnreleasedID   = "rs-deleted-unreleased"
		freshTimedOutID       = "rs-fresh-timedout"
		unconfirmedTimedOutID = "rs-unconfirmed-timedout"
	)
	// Retire: true on every fixture skips Tick's own retire-forcing Save
	// (o.Config.MaxLifetimeSeconds has long since passed relative to `old`)
	// - irrelevant to what this test exercises, and this package's fake
	// clientset does not simulate real resourceVersion bumps the way a real
	// cluster does (see docs/operations.md's own note that only
	// `make integration` exercises real CAS), so a redundant Save here would
	// spuriously conflict.
	for _, a := range []lifecycle.Allocation{
		{ID: timedOutID, Phase: lifecycle.TimedOut, Deadline: old, MaxAttempts: 3, Retire: true, TerminalAt: old, RegistrationCleared: true},
		{ID: deletedReleasedID, Phase: lifecycle.Deleted, Deadline: old, MaxAttempts: 3, Retire: true, TerminalAt: old, RegistrationCleared: true},
		{ID: deletedUnreleasedID, Phase: lifecycle.Deleted, Deadline: old, MaxAttempts: 3, Retire: true, TerminalAt: old, RegistrationCleared: true},
		{ID: freshTimedOutID, Phase: lifecycle.TimedOut, Deadline: old, MaxAttempts: 3, Retire: true, TerminalAt: time.Now(), RegistrationCleared: true},
		// Past retention and otherwise identical to timedOutID, but its
		// runner deregistration was never confirmed - e.g. a hypothetical
		// future code path reaching TimedOut/Deleted without going through
		// Controller.deregister. Must never be pruned, no matter how old:
		// doing so would discard the only local evidence a claimed GitHub
		// runner registration might still need cleanup, with nothing else in
		// this codebase able to rediscover that afterward.
		{ID: unconfirmedTimedOutID, Phase: lifecycle.TimedOut, Deadline: old, MaxAttempts: 3, Retire: true, TerminalAt: old, RegistrationCleared: false},
	} {
		if _, e := o.Store.Save(ctx, a, ""); e != nil {
			t.Fatal(e)
		}
	}

	cm, f, e := o.loadFleet(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{timedOutID, deletedReleasedID, deletedUnreleasedID, freshTimedOutID, unconfirmedTimedOutID} {
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
	if _, e := o.Store.Load(ctx, unconfirmedTimedOutID); e != nil {
		t.Fatal("a terminal record whose runner deregistration was never confirmed must never be pruned", e)
	}
}
