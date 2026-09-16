package operator

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

// slowCloud records, for every Create/Observe call, how far in the future
// the context's deadline was (0 if none) and blocks for `delay` before
// returning - deterministic enough to distinguish "Tick ran N of these
// sequentially" from "Tick ran them concurrently" by wall-clock time,
// without depending on real timing tolerances tighter than delay itself.
type slowCloud struct {
	mu               sync.Mutex
	delay            time.Duration
	createDeadlines  []time.Duration
	observeDeadlines []time.Duration
	rejectIDs        map[string]bool
}

func (p *slowCloud) Create(ctx context.Context, a lifecycle.Allocation) (string, error) {
	p.record(ctx, &p.createDeadlines)
	time.Sleep(p.delay)
	if p.rejectIDs[a.ID] {
		return "", lifecycle.ErrCapacity
	}
	return "vm-" + a.ID, nil
}
func (p *slowCloud) Observe(ctx context.Context, a lifecycle.Allocation) (lifecycle.Observation, error) {
	p.record(ctx, &p.observeDeadlines)
	return lifecycle.Observation{Known: true, Exists: true, ResourceID: "vm-" + a.ID}, nil
}
func (p *slowCloud) Delete(context.Context, lifecycle.Allocation) error { return nil }
func (p *slowCloud) record(ctx context.Context, into *[]time.Duration) {
	d := time.Duration(0)
	if dl, ok := ctx.Deadline(); ok {
		d = time.Until(dl)
	}
	p.mu.Lock()
	*into = append(*into, d)
	p.mu.Unlock()
}

// concurrentOperatorFixture mirrors controlledOperator's fake-clientset
// resourceVersion-assigning reactor, but with a mutex instead of a bare
// int++, since these tests genuinely exercise concurrent Save calls across
// different allocations - unlike every other (sequential) test in this
// package, a race here would be real, not a false positive.
func concurrentOperatorFixture(maxRunners int) (*Operator, *slowCloud) {
	cfg := namedCredentialConfig()
	cfg.MaxRunners = maxRunners
	cfg.Catalog = placement.Catalog{Complete: map[string]bool{"a-aws": true, "z-gcp": true}, Offerings: []placement.Offering{{ID: "pool", Provider: "a-aws", Region: "r", Zone: "z", Machine: "machine", Image: "image", CPU: 1, MemoryMiB: 1, Architecture: "amd64", Spot: true, PriceMicros: 1, Currency: "USD", ObservedAt: time.Now()}}}
	client := fake.NewClientset()
	var mu sync.Mutex
	version := 0
	client.PrependReactor("*", "configmaps", func(action kt.Action) (bool, runtime.Object, error) {
		var object metav1.Object
		switch action := action.(type) {
		case kt.CreateAction:
			object = action.GetObject().(metav1.Object)
		case kt.UpdateAction:
			object = action.GetObject().(metav1.Object)
		default:
			return false, nil, nil
		}
		mu.Lock()
		version++
		object.SetResourceVersion(strconv.Itoa(version))
		mu.Unlock()
		return false, nil, nil
	})
	op := New(cfg, client, nil)
	cloud := &slowCloud{}
	op.Controller.Providers["a-aws"] = cloud
	return op, cloud
}

// A batch of Pending-phase allocations admitted together (the exact
// "burst of new demand" scenario issue #144 described) must be created
// concurrently, not serialized behind one another in the same Tick - N
// allocations each taking `delay` must finish this Tick in close to one
// `delay`, not N of them.
func TestTickCreatesConcurrentAllocationsInParallelNotSerially(t *testing.T) {
	const n = 5
	const delay = 150 * time.Millisecond
	op, cloud := concurrentOperatorFixture(n)
	cloud.delay = delay
	ctx := context.Background()
	if _, err := op.HandleDesiredRunnerCount(ctx, n); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := op.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if len(cloud.createDeadlines) != n {
		t.Fatalf("expected %d Create calls, got %d", n, len(cloud.createDeadlines))
	}
	// Sequential execution would take at least n*delay (750ms here); a
	// generous ceiling well under that still clearly distinguishes
	// "ran concurrently" from "ran one at a time" without being flaky.
	if elapsed >= delay*(n-1) {
		t.Fatalf("Tick took %v for %d allocations at %v each - looks serialized, not concurrent", elapsed, n, delay)
	}
}

// The create-path budget (Pending phase, about to call Create) must
// actually reach the provider uncapped by the old blanket 30s - it should
// be minutes, not seconds. An Observe-only call (Running phase) has no
// evidence it needs more than the original 30s budget and must not
// silently grow too, which would multiply worst-case Tick duration for no
// reason.
func TestTickGivesCreateALongerBudgetThanObserve(t *testing.T) {
	op, cloud := concurrentOperatorFixture(2)
	ctx := context.Background()
	// First allocation: admit and step it all the way to Running.
	if _, err := op.HandleDesiredRunnerCount(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := op.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	allocs, err := op.Store.List(ctx)
	if err != nil || len(allocs) != 1 || allocs[0].Phase != lifecycle.Running {
		t.Fatal("expected the first allocation to already be Running", allocs, err)
	}
	cloud.mu.Lock()
	cloud.createDeadlines, cloud.observeDeadlines = nil, nil
	cloud.mu.Unlock()
	// Second allocation: admit fresh, so this next Tick steps one
	// Running-phase (Observe) allocation and one Pending-phase (Create)
	// allocation together.
	if _, err := op.HandleDesiredRunnerCount(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := op.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(cloud.createDeadlines) != 1 || len(cloud.observeDeadlines) != 1 {
		t.Fatalf("expected exactly one Create and one Observe, got %d/%d", len(cloud.createDeadlines), len(cloud.observeDeadlines))
	}
	if cloud.createDeadlines[0] < time.Minute {
		t.Fatalf("Create's budget was not actually raised: %v", cloud.createDeadlines[0])
	}
	if cloud.observeDeadlines[0] >= time.Minute || cloud.observeDeadlines[0] <= 0 {
		t.Fatalf("Observe's budget changed unexpectedly: %v", cloud.observeDeadlines[0])
	}
}

// Concurrent capacity rejections across allocations sharing the same pool
// must all land in Controller.Cooldowns - a lost update here (a real risk
// once Step calls run concurrently against a shared map) would let a
// cooled-down pool get retried early.
func TestTickAggregatesCooldownsFromConcurrentCapacityRejections(t *testing.T) {
	const n = 4
	op, cloud := concurrentOperatorFixture(n)
	cloud.delay = 30 * time.Millisecond
	cloud.rejectIDs = map[string]bool{}
	ctx := context.Background()
	if _, err := op.HandleDesiredRunnerCount(ctx, n); err != nil {
		t.Fatal(err)
	}
	_, f, err := op.loadFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Pending) != n {
		t.Fatalf("expected %d pending allocations, got %d", n, len(f.Pending))
	}
	for id := range f.Pending {
		cloud.rejectIDs[id] = true
	}
	if err := op.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(cloud.createDeadlines) != n {
		t.Fatalf("expected %d Create attempts, got %d", n, len(cloud.createDeadlines))
	}
	if until, ok := op.Controller.Cooldowns["pool"]; !ok || !until.After(time.Now()) {
		t.Fatalf("expected pool cooldown recorded from concurrent capacity rejections, got %v", op.Controller.Cooldowns)
	}
}

// An allocation the Store knows about but the fleet ConfigMap's f.Created
// map has no entry for (the two falling out of sync - a manual kubectl
// edit, backup/restore) must not strand the goroutines Tick already spawned
// for other, valid allocations earlier in the same loop: Tick must still
// join every goroutine it started before returning, not abandon them by
// returning directly from inside the loop (which would also release o.mu
// while they're still running, unblocking a subsequent Tick to race them).
func TestTickJoinsAlreadySpawnedGoroutinesWhenALaterAllocationHasNoOrigin(t *testing.T) {
	const n = 3
	const delay = 200 * time.Millisecond
	op, cloud := concurrentOperatorFixture(n)
	cloud.delay = delay
	ctx := context.Background()
	if _, err := op.HandleDesiredRunnerCount(ctx, n); err != nil {
		t.Fatal(err)
	}
	cm, f, err := op.loadFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(f.Created))
	for id := range f.Created {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// Store.List (backing the allocs range in Tick) returns allocations
	// name-sorted, so breaking the last-sorted ID guarantees the other
	// n-1 allocations' goroutines are already spawned by the time Tick
	// reaches this one.
	broken := ids[len(ids)-1]
	delete(f.Created, broken)
	if err := op.saveFleet(ctx, cm, f); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err = op.Tick(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error for the allocation missing its durable lifetime origin")
	}
	// If the already-spawned goroutines for the other n-1 allocations were
	// stranded instead of joined, Tick would return almost immediately -
	// well under delay. Joining them means Tick cannot return before delay
	// has elapsed.
	if elapsed < delay {
		t.Fatalf("Tick returned after %v, before its still-running goroutines (delay %v) could have finished - they were stranded, not joined", elapsed, delay)
	}
	if len(cloud.createDeadlines) != n-1 {
		t.Fatalf("expected %d Create calls from the still-valid allocations, got %d", n-1, len(cloud.createDeadlines))
	}
}
