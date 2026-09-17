package operator

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/prices"
	"github.com/tsouza/runnerscout/internal/state"
	"k8s.io/client-go/kubernetes/fake"
)

// fakeAWSPrices is a local awsPriceObserver test double, mirroring the fakes
// used for githubJobsClient in retry_test.go.
type fakeAWSPrices struct {
	observe func(ctx context.Context, region, zone, instanceType string) (prices.Quote, error)
}

func (f *fakeAWSPrices) Observe(ctx context.Context, region, zone, instanceType string) (prices.Quote, error) {
	return f.observe(ctx, region, zone, instanceType)
}

// fakeAzurePrices is a local azurePriceObserver test double, mirroring
// fakeAWSPrices.
type fakeAzurePrices struct {
	observe func(ctx context.Context, region, zone, instanceType string) (prices.Quote, error)
}

func (f *fakeAzurePrices) Observe(ctx context.Context, region, zone, instanceType string) (prices.Quote, error) {
	return f.observe(ctx, region, zone, instanceType)
}

func awsOffering(id string) placement.Offering {
	return placement.Offering{ID: id, Provider: "aws", Region: "us-east-1", Zone: "us-east-1a", Machine: "m5.large", Image: "i", CPU: 1, MemoryMiB: 1, Architecture: "amd64", Spot: true, PriceMicros: 999999, Currency: "USD", ObservedAt: time.Now().Add(-time.Hour)}
}

func azureOffering(id string) placement.Offering {
	return placement.Offering{ID: id, Provider: "azure", Region: "eastus", Zone: "1", Machine: "Standard_D2s_v3", Image: "i", CPU: 1, MemoryMiB: 1, Architecture: "amd64", Spot: true, PriceMicros: 999999, Currency: "USD", ObservedAt: time.Now().Add(-time.Hour)}
}

// fakeGCPPrices is a local gcpPriceObserver test double, mirroring
// fakeAWSPrices/fakeAzurePrices.
type fakeGCPPrices struct {
	observe func(ctx context.Context, coreSkuID, ramSkuID string, cpu, memoryMiB int) (prices.Quote, error)
}

func (f *fakeGCPPrices) Observe(ctx context.Context, coreSkuID, ramSkuID string, cpu, memoryMiB int) (prices.Quote, error) {
	return f.observe(ctx, coreSkuID, ramSkuID, cpu, memoryMiB)
}

// gcpOffering mirrors awsOffering/azureOffering, with GCPSkuRefs already
// pinned - the precondition refreshGCPPrices requires before it will ever
// touch a GCP offering (see refreshGCPPrices's own doc comment).
func gcpOffering(id string) placement.Offering {
	return placement.Offering{ID: id, Provider: "gcp", Region: "us-central1", Zone: "us-central1-a", Machine: "e2-medium", Image: "i", CPU: 2, MemoryMiB: 4096, Architecture: "amd64", Spot: true, PriceMicros: 999999, Currency: "USD", ObservedAt: time.Now().Add(-time.Hour), GCPSkuRefs: &placement.GCPSkuRefs{CoreSkuID: "core-id", RamSkuID: "ram-id"}}
}

func TestRefreshAWSPricesNilObserverLeavesCatalogUnmodified(t *testing.T) {
	o := &Operator{}
	catalog := placement.Catalog{Offerings: []placement.Offering{awsOffering("a"), azureOffering("b")}, Complete: map[string]bool{"aws": true, "azure": true}}
	before := catalog.Offerings[0]
	got := o.refreshAWSPrices(context.Background(), catalog)
	if len(got.Offerings) != 2 || !reflect.DeepEqual(got.Offerings[0], before) {
		t.Fatalf("catalog changed with nil AWSPrices: %+v", got)
	}
	if !reflect.DeepEqual(got.Offerings[1], catalog.Offerings[1]) {
		t.Fatalf("non-AWS offering changed: %+v", got.Offerings[1])
	}
}

func TestRefreshAWSPricesUpdatesSuccessfulAWSOffering(t *testing.T) {
	want := prices.Quote{PriceMicros: 42, Currency: "USD", ObservedAt: time.Now()}
	o := &Operator{AWSPrices: &fakeAWSPrices{observe: func(ctx context.Context, region, zone, instanceType string) (prices.Quote, error) {
		if region != "us-east-1" || zone != "us-east-1a" || instanceType != "m5.large" {
			t.Fatalf("unexpected observe args: %s %s %s", region, zone, instanceType)
		}
		return want, nil
	}}}
	catalog := placement.Catalog{Offerings: []placement.Offering{awsOffering("a")}}
	got := o.refreshAWSPrices(context.Background(), catalog)
	offering := got.Offerings[0]
	if offering.PriceMicros != want.PriceMicros || offering.Currency != want.Currency || !offering.ObservedAt.Equal(want.ObservedAt) {
		t.Fatalf("offering not refreshed from quote: %+v", offering)
	}
}

// TestRefreshAWSPricesNeverOverwritesOnDemandOffering proves an on-demand
// (Spot: false) AWS offering's catalog price survives refreshAWSPrices
// untouched. AWSSpotClient.Observe only ever returns a Spot price (see its
// own doc comment), so overwriting an on-demand offering's price with it
// would silently corrupt the one price placement.Choose's MaxPriceMicros
// ceiling actually compares against - a real gap, not a case this
// feature was ever exercised against before.
func TestRefreshAWSPricesNeverOverwritesOnDemandOffering(t *testing.T) {
	o := &Operator{AWSPrices: &fakeAWSPrices{observe: func(ctx context.Context, region, zone, instanceType string) (prices.Quote, error) {
		return prices.Quote{PriceMicros: 1, Currency: "USD", ObservedAt: time.Now()}, nil
	}}}
	onDemand := awsOffering("a")
	onDemand.Spot = false
	before := onDemand
	catalog := placement.Catalog{Offerings: []placement.Offering{onDemand}}
	got := o.refreshAWSPrices(context.Background(), catalog)
	if !reflect.DeepEqual(got.Offerings[0], before) {
		t.Fatalf("on-demand offering was overwritten by a Spot quote: %+v", got.Offerings[0])
	}
}

func TestRefreshAWSPricesIsolatesFailurePerOffering(t *testing.T) {
	staleObservedAt := time.Now().Add(-time.Hour)
	failing := awsOffering("fails")
	failing.ObservedAt = staleObservedAt
	failing.Machine = "m5.fail" // distinct instance type so the fake can fail selectively by request shape
	succeeding := awsOffering("succeeds")
	untouched := azureOffering("azure")
	want := prices.Quote{PriceMicros: 7, Currency: "USD", ObservedAt: time.Now()}
	o := &Operator{AWSPrices: &fakeAWSPrices{observe: func(ctx context.Context, region, zone, instanceType string) (prices.Quote, error) {
		if instanceType == "m5.fail" {
			return prices.Quote{}, errors.New("observation failed")
		}
		return want, nil
	}}}
	catalog := placement.Catalog{Offerings: []placement.Offering{failing, succeeding, untouched}}
	got := o.refreshAWSPrices(context.Background(), catalog)

	gotFailing, gotSucceeding, gotUntouched := got.Offerings[0], got.Offerings[1], got.Offerings[2]
	if !gotFailing.ObservedAt.IsZero() {
		t.Fatalf("failing offering must have ObservedAt zeroed, got %v", gotFailing.ObservedAt)
	}
	if gotFailing.PriceMicros != failing.PriceMicros || gotFailing.Currency != failing.Currency {
		t.Fatalf("failing offering price/currency should be left alone: %+v", gotFailing)
	}
	if gotSucceeding.PriceMicros != want.PriceMicros || gotSucceeding.Currency != want.Currency || !gotSucceeding.ObservedAt.Equal(want.ObservedAt) {
		t.Fatalf("succeeding AWS offering not refreshed: %+v", gotSucceeding)
	}
	if !reflect.DeepEqual(gotUntouched, untouched) {
		t.Fatalf("non-AWS offering must never be touched: %+v", gotUntouched)
	}
}

// A real production incident: a leader pod stuck with idle CPU and no log
// line for 49+ minutes, because nothing bounded a sequential external call
// under an unbounded, cancel-only ctx (runLeader's own, ultimately what
// refreshAWSPrices is called with via HandleDesiredRunnerCount from the
// listener's own message loop). This is the same mechanism as
// externalCallBudget's other two call sites (runLeader's own startup
// sequence, pruneTerminalAllocations's DeregisterRunner loop) - proven once
// here since all three refresh* functions share this exact wrapping. A
// stalled offering must not block a later one in the same refresh pass.
func TestRefreshAWSPricesBoundsAnUnresponsiveObserver(t *testing.T) {
	previous := externalCallBudget
	externalCallBudget = 100 * time.Millisecond
	defer func() { externalCallBudget = previous }()

	hangs := awsOffering("hangs")
	hangs.Machine = "m5.hangs"
	after := awsOffering("after-hang")
	var calls []string
	o := &Operator{AWSPrices: &fakeAWSPrices{observe: func(ctx context.Context, region, zone, instanceType string) (prices.Quote, error) {
		calls = append(calls, instanceType)
		if instanceType == "m5.hangs" {
			<-ctx.Done()
			return prices.Quote{}, ctx.Err()
		}
		return prices.Quote{PriceMicros: 7, Currency: "USD", ObservedAt: time.Now()}, nil
	}}}
	catalog := placement.Catalog{Offerings: []placement.Offering{hangs, after}}

	done := make(chan placement.Catalog, 1)
	go func() { done <- o.refreshAWSPrices(context.Background(), catalog) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refreshAWSPrices did not bound the stalled observation - it hung past externalCallBudget")
	}
	if len(calls) != 2 {
		t.Fatal("a stalled offering must not prevent a later one from being observed", calls)
	}
}

func TestHandleDesiredRunnerCountAdmitsViaRefreshedAWSPrice(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}
	cfg.Requirements = placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: []string{"aws"}, Regions: []string{"us-east-1"}, Policy: "lowest-price"}
	// The static catalog's ObservedAt is well past placement.MaxPriceAge, so
	// this offering is inadmissible on the stale data alone.
	stale := awsOffering("pool")
	stale.PriceMicros = 1
	stale.ObservedAt = time.Now().Add(-2 * placement.MaxPriceAge)
	cfg.Catalog = placement.Catalog{Complete: map[string]bool{"aws": true}, Offerings: []placement.Offering{stale}}
	s := &state.Kubernetes{Maps: k.CoreV1().ConfigMaps("test"), Owner: "test"}
	fresh := prices.Quote{PriceMicros: 1, Currency: "USD", ObservedAt: time.Now()}
	o := &Operator{Config: cfg, Client: k, Store: s, AWSPrices: &fakeAWSPrices{observe: func(ctx context.Context, region, zone, instanceType string) (prices.Quote, error) {
		return fresh, nil
	}}}
	n, e := o.HandleDesiredRunnerCount(ctx, 1)
	if e != nil || n != 1 {
		t.Fatalf("expected admission via live-refreshed AWS price, got n=%d e=%v", n, e)
	}
}

func TestRefreshAzurePricesNilObserverLeavesCatalogUnmodified(t *testing.T) {
	o := &Operator{}
	catalog := placement.Catalog{Offerings: []placement.Offering{awsOffering("a"), azureOffering("b")}, Complete: map[string]bool{"aws": true, "azure": true}}
	before := catalog.Offerings[1]
	got := o.refreshAzurePrices(context.Background(), catalog)
	if len(got.Offerings) != 2 || !reflect.DeepEqual(got.Offerings[1], before) {
		t.Fatalf("catalog changed with nil AzurePrices: %+v", got)
	}
	if !reflect.DeepEqual(got.Offerings[0], catalog.Offerings[0]) {
		t.Fatalf("non-Azure offering changed: %+v", got.Offerings[0])
	}
}

func TestRefreshAzurePricesUpdatesSuccessfulAzureOffering(t *testing.T) {
	want := prices.Quote{PriceMicros: 42, Currency: "USD", ObservedAt: time.Now()}
	o := &Operator{AzurePrices: &fakeAzurePrices{observe: func(ctx context.Context, region, zone, instanceType string) (prices.Quote, error) {
		if region != "eastus" || zone != "1" || instanceType != "Standard_D2s_v3" {
			t.Fatalf("unexpected observe args: %s %s %s", region, zone, instanceType)
		}
		return want, nil
	}}}
	catalog := placement.Catalog{Offerings: []placement.Offering{azureOffering("a")}}
	got := o.refreshAzurePrices(context.Background(), catalog)
	offering := got.Offerings[0]
	if offering.PriceMicros != want.PriceMicros || offering.Currency != want.Currency || !offering.ObservedAt.Equal(want.ObservedAt) {
		t.Fatalf("offering not refreshed from quote: %+v", offering)
	}
}

// TestRefreshAzurePricesNeverOverwritesOnDemandOffering is
// TestRefreshAWSPricesNeverOverwritesOnDemandOffering's exact Azure
// counterpart: AzureSpotClient.Observe only ever returns a Spot price too.
func TestRefreshAzurePricesNeverOverwritesOnDemandOffering(t *testing.T) {
	o := &Operator{AzurePrices: &fakeAzurePrices{observe: func(ctx context.Context, region, zone, instanceType string) (prices.Quote, error) {
		return prices.Quote{PriceMicros: 1, Currency: "USD", ObservedAt: time.Now()}, nil
	}}}
	onDemand := azureOffering("a")
	onDemand.Spot = false
	before := onDemand
	catalog := placement.Catalog{Offerings: []placement.Offering{onDemand}}
	got := o.refreshAzurePrices(context.Background(), catalog)
	if !reflect.DeepEqual(got.Offerings[0], before) {
		t.Fatalf("on-demand offering was overwritten by a Spot quote: %+v", got.Offerings[0])
	}
}

func TestRefreshAzurePricesIsolatesFailurePerOffering(t *testing.T) {
	staleObservedAt := time.Now().Add(-time.Hour)
	failing := azureOffering("fails")
	failing.ObservedAt = staleObservedAt
	failing.Machine = "Standard_Fail_v3" // distinct instance type so the fake can fail selectively by request shape
	succeeding := azureOffering("succeeds")
	untouched := awsOffering("aws")
	want := prices.Quote{PriceMicros: 7, Currency: "USD", ObservedAt: time.Now()}
	o := &Operator{AzurePrices: &fakeAzurePrices{observe: func(ctx context.Context, region, zone, instanceType string) (prices.Quote, error) {
		if instanceType == "Standard_Fail_v3" {
			return prices.Quote{}, errors.New("observation failed")
		}
		return want, nil
	}}}
	catalog := placement.Catalog{Offerings: []placement.Offering{failing, succeeding, untouched}}
	got := o.refreshAzurePrices(context.Background(), catalog)

	gotFailing, gotSucceeding, gotUntouched := got.Offerings[0], got.Offerings[1], got.Offerings[2]
	if !gotFailing.ObservedAt.IsZero() {
		t.Fatalf("failing offering must have ObservedAt zeroed, got %v", gotFailing.ObservedAt)
	}
	if gotFailing.PriceMicros != failing.PriceMicros || gotFailing.Currency != failing.Currency {
		t.Fatalf("failing offering price/currency should be left alone: %+v", gotFailing)
	}
	if gotSucceeding.PriceMicros != want.PriceMicros || gotSucceeding.Currency != want.Currency || !gotSucceeding.ObservedAt.Equal(want.ObservedAt) {
		t.Fatalf("succeeding Azure offering not refreshed: %+v", gotSucceeding)
	}
	if !reflect.DeepEqual(gotUntouched, untouched) {
		t.Fatalf("non-Azure offering must never be touched: %+v", gotUntouched)
	}
}

func TestHandleDesiredRunnerCountAdmitsViaRefreshedAzurePrice(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}
	cfg.Requirements = placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: []string{"azure"}, Regions: []string{"eastus"}, Policy: "lowest-price"}
	// The static catalog's ObservedAt is well past placement.MaxPriceAge, so
	// this offering is inadmissible on the stale data alone.
	stale := azureOffering("pool")
	stale.PriceMicros = 1
	stale.ObservedAt = time.Now().Add(-2 * placement.MaxPriceAge)
	cfg.Catalog = placement.Catalog{Complete: map[string]bool{"azure": true}, Offerings: []placement.Offering{stale}}
	s := &state.Kubernetes{Maps: k.CoreV1().ConfigMaps("test"), Owner: "test"}
	fresh := prices.Quote{PriceMicros: 1, Currency: "USD", ObservedAt: time.Now()}
	o := &Operator{Config: cfg, Client: k, Store: s, AzurePrices: &fakeAzurePrices{observe: func(ctx context.Context, region, zone, instanceType string) (prices.Quote, error) {
		return fresh, nil
	}}}
	n, e := o.HandleDesiredRunnerCount(ctx, 1)
	if e != nil || n != 1 {
		t.Fatalf("expected admission via live-refreshed Azure price, got n=%d e=%v", n, e)
	}
}

func TestRefreshGCPPricesNilObserverLeavesCatalogUnmodified(t *testing.T) {
	o := &Operator{}
	catalog := placement.Catalog{Offerings: []placement.Offering{gcpOffering("a"), awsOffering("b")}, Complete: map[string]bool{"gcp": true, "aws": true}}
	before := catalog.Offerings[0]
	got := o.refreshGCPPrices(context.Background(), catalog)
	if len(got.Offerings) != 2 || !reflect.DeepEqual(got.Offerings[0], before) {
		t.Fatalf("catalog changed with nil GCPPrices: %+v", got)
	}
	if !reflect.DeepEqual(got.Offerings[1], catalog.Offerings[1]) {
		t.Fatalf("non-GCP offering changed: %+v", got.Offerings[1])
	}
}

func TestRefreshGCPPricesUpdatesOfferingWithPinnedSkuRefs(t *testing.T) {
	want := prices.Quote{PriceMicros: 42, Currency: "USD", ObservedAt: time.Now()}
	o := &Operator{GCPPrices: &fakeGCPPrices{observe: func(ctx context.Context, coreSkuID, ramSkuID string, cpu, memoryMiB int) (prices.Quote, error) {
		if coreSkuID != "core-id" || ramSkuID != "ram-id" || cpu != 2 || memoryMiB != 4096 {
			t.Fatalf("unexpected observe args: %s %s %d %d", coreSkuID, ramSkuID, cpu, memoryMiB)
		}
		return want, nil
	}}}
	catalog := placement.Catalog{Offerings: []placement.Offering{gcpOffering("a")}}
	got := o.refreshGCPPrices(context.Background(), catalog)
	offering := got.Offerings[0]
	if offering.PriceMicros != want.PriceMicros || offering.Currency != want.Currency || !offering.ObservedAt.Equal(want.ObservedAt) {
		t.Fatalf("offering not refreshed from quote: %+v", offering)
	}
}

// TestRefreshGCPPricesNeverTouchesOfferingWithoutPinnedSkuRefs proves a GCP
// offering with no GCPSkuRefs stays on its static catalog price even when
// GCPPrices is configured and would otherwise happily answer for it -
// mirroring refreshAWSPrices/refreshAzurePrices's own "never touch what it
// wasn't asked to observe" discipline, applied to GCP's per-offering
// (rather than per-provider) opt-in signal.
func TestRefreshGCPPricesNeverTouchesOfferingWithoutPinnedSkuRefs(t *testing.T) {
	o := &Operator{GCPPrices: &fakeGCPPrices{observe: func(ctx context.Context, coreSkuID, ramSkuID string, cpu, memoryMiB int) (prices.Quote, error) {
		t.Fatal("Observe must never be called for an offering without GCPSkuRefs")
		return prices.Quote{}, nil
	}}}
	unpinned := gcpOffering("a")
	unpinned.GCPSkuRefs = nil
	before := unpinned
	catalog := placement.Catalog{Offerings: []placement.Offering{unpinned}}
	got := o.refreshGCPPrices(context.Background(), catalog)
	if !reflect.DeepEqual(got.Offerings[0], before) {
		t.Fatalf("unpinned GCP offering was touched: %+v", got.Offerings[0])
	}
}

func TestRefreshGCPPricesIsolatesFailurePerOffering(t *testing.T) {
	staleObservedAt := time.Now().Add(-time.Hour)
	failing := gcpOffering("fails")
	failing.ObservedAt = staleObservedAt
	failing.GCPSkuRefs = &placement.GCPSkuRefs{CoreSkuID: "fail-core", RamSkuID: "fail-ram"}
	succeeding := gcpOffering("succeeds")
	untouched := awsOffering("aws")
	want := prices.Quote{PriceMicros: 7, Currency: "USD", ObservedAt: time.Now()}
	o := &Operator{GCPPrices: &fakeGCPPrices{observe: func(ctx context.Context, coreSkuID, ramSkuID string, cpu, memoryMiB int) (prices.Quote, error) {
		if coreSkuID == "fail-core" {
			return prices.Quote{}, errors.New("observation failed")
		}
		return want, nil
	}}}
	catalog := placement.Catalog{Offerings: []placement.Offering{failing, succeeding, untouched}}
	got := o.refreshGCPPrices(context.Background(), catalog)

	gotFailing, gotSucceeding, gotUntouched := got.Offerings[0], got.Offerings[1], got.Offerings[2]
	if !gotFailing.ObservedAt.IsZero() {
		t.Fatalf("failing offering must have ObservedAt zeroed, got %v", gotFailing.ObservedAt)
	}
	if gotFailing.PriceMicros != failing.PriceMicros || gotFailing.Currency != failing.Currency {
		t.Fatalf("failing offering price/currency should be left alone: %+v", gotFailing)
	}
	if gotSucceeding.PriceMicros != want.PriceMicros || gotSucceeding.Currency != want.Currency || !gotSucceeding.ObservedAt.Equal(want.ObservedAt) {
		t.Fatalf("succeeding GCP offering not refreshed: %+v", gotSucceeding)
	}
	if !reflect.DeepEqual(gotUntouched, untouched) {
		t.Fatalf("non-GCP offering must never be touched: %+v", gotUntouched)
	}
}

func TestHandleDesiredRunnerCountAdmitsViaRefreshedGCPPrice(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}
	cfg.Requirements = placement.Requirements{CPU: 2, MemoryMiB: 4096, Architecture: "amd64", MaxPriceMicros: 100, Providers: []string{"gcp"}, Regions: []string{"us-central1"}, Policy: "lowest-price"}
	// The static catalog's ObservedAt is well past placement.MaxPriceAge, so
	// this offering is inadmissible on the stale data alone.
	stale := gcpOffering("pool")
	stale.PriceMicros = 1
	stale.ObservedAt = time.Now().Add(-2 * placement.MaxPriceAge)
	cfg.Catalog = placement.Catalog{Complete: map[string]bool{"gcp": true}, Offerings: []placement.Offering{stale}}
	s := &state.Kubernetes{Maps: k.CoreV1().ConfigMaps("test"), Owner: "test"}
	fresh := prices.Quote{PriceMicros: 1, Currency: "USD", ObservedAt: time.Now()}
	o := &Operator{Config: cfg, Client: k, Store: s, GCPPrices: &fakeGCPPrices{observe: func(ctx context.Context, coreSkuID, ramSkuID string, cpu, memoryMiB int) (prices.Quote, error) {
		return fresh, nil
	}}}
	n, e := o.HandleDesiredRunnerCount(ctx, 1)
	if e != nil || n != 1 {
		t.Fatalf("expected admission via live-refreshed GCP price, got n=%d e=%v", n, e)
	}
}
