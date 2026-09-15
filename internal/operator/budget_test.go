package operator

import (
	"context"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/provider"
	"github.com/tsouza/runnerscout/internal/state"
	"k8s.io/client-go/kubernetes/fake"
)

func validConfigFixture() Config {
	cfg := Config{Name: "test", Namespace: "test", GitHubURL: "https://github.com/example", ScaleSetID: 1, MaxRunners: 1, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}
	cfg.Requirements = placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 1, Providers: []string{"p"}, Regions: []string{"r"}, Policy: "lowest-price"}
	cfg.Providers = map[string]provider.Config{"p": {Kind: "gcp", Owner: "test", Subnet: "s", Project: "proj"}}
	return cfg
}

func TestValidateRejectsNegativeBudget(t *testing.T) {
	cfg := validConfigFixture()
	// A negative ceiling must be rejected outright, never silently treated as
	// unbounded - the admission gate only checks BudgetDailyMicros > 0, so a
	// negative value would otherwise pass through Validate and behave
	// identically to no budget at all, contradicting an operator's intent.
	cfg.BudgetDailyMicros = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("negative BudgetDailyMicros must fail validation")
	}
	cfg.BudgetDailyMicros = 0
	if err := cfg.Validate(); err != nil {
		t.Fatal("zero (unbounded) BudgetDailyMicros must remain valid", err)
	}
	cfg.BudgetDailyMicros = 1
	if err := cfg.Validate(); err != nil {
		t.Fatal("positive BudgetDailyMicros must remain valid", err)
	}
}

func TestValidateRejectsMaxPriceMicrosAboveOverflowCeiling(t *testing.T) {
	// Without an upper bound, an extreme maxPriceMicros times the maximum
	// allowed lifetime (21600s) overflows reservationMicros's int64
	// multiplication and goes negative, which would make the budget gate
	// wrongly refuse every admission as BudgetExhausted even though the
	// real budget is not exhausted. Validate must reject the input outright
	// rather than let that silent overflow happen downstream.
	cfg := validConfigFixture()
	cfg.Requirements.MaxPriceMicros = 100_000_000_001
	if err := cfg.Validate(); err == nil {
		t.Fatal("maxPriceMicros above the overflow ceiling must fail validation")
	}
	cfg.Requirements.MaxPriceMicros = 100_000_000_000
	if err := cfg.Validate(); err != nil {
		t.Fatal("maxPriceMicros at the ceiling must remain valid", err)
	}
}

func TestReservationMicrosRoundsUpToNeverUnderReserve(t *testing.T) {
	// 1000 micros/hour for exactly one hour reserves exactly 1000: no
	// rounding needed when the lifetime is an even multiple of an hour.
	if got := reservationMicros(1000, 3600); got != 1000 {
		t.Fatal(got)
	}
	// 1000 micros/hour for 1800s (half an hour) must round up to 500, never
	// down - underreserving would let actual worst-case spend exceed budget.
	if got := reservationMicros(1000, 1800); got != 500 {
		t.Fatal(got)
	}
	// A lifetime that doesn't evenly divide an hour still rounds up rather
	// than truncating away real fractional cost.
	if got := reservationMicros(3600, 1); got != 1 {
		t.Fatal(got)
	}
}

func TestSpentTodayExcludesTimedOutAndOtherDays(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	f := fleet{
		Created: map[string]time.Time{
			"today-active":    now.Add(-time.Hour),
			"today-timed-out": now.Add(-time.Hour),
			"today-unknown":   now.Add(-time.Hour), // no matching allocation record
			"yesterday":       now.Add(-25 * time.Hour),
		},
		Reserved: map[string]int64{
			"today-active":    100,
			"today-timed-out": 200,
			"today-unknown":   300,
			"yesterday":       400,
		},
	}
	allocs := []lifecycle.Allocation{
		{ID: "today-active", Phase: lifecycle.Creating},
		{ID: "today-timed-out", Phase: lifecycle.TimedOut},
	}
	got := spentToday(f, allocs, now)
	// today-active (100) + today-unknown (300, no allocation record found yet
	// so it cannot be proven TimedOut) - excludes today-timed-out (proven to
	// have never held cloud resources) and yesterday (wrong day).
	if want := int64(400); got != want {
		t.Fatal(got, want)
	}
}

func TestBudgetGateAdmitsUpToCeilingThenRefuses(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 3600}
	cfg.Requirements = placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 1000, Providers: []string{"p"}, Regions: []string{"r"}, Policy: "lowest-price"}
	cfg.Catalog = placement.Catalog{Complete: map[string]bool{"p": true}, Offerings: []placement.Offering{{ID: "pool", Provider: "p", Region: "r", Zone: "z", Machine: "m", Image: "i", CPU: 1, MemoryMiB: 1, Architecture: "amd64", PriceMicros: 1, Currency: "USD", Spot: true, ObservedAt: time.Now()}}}
	cfg.BudgetDailyMicros = 3000 // exactly 3 reservations at 1000 each
	s := &state.Kubernetes{Maps: k.CoreV1().ConfigMaps("test"), Owner: "test"}
	o := &Operator{Config: cfg, Client: k, Store: s}

	n, e := o.HandleDesiredRunnerCount(ctx, 5)
	if e != nil || n != 3 {
		t.Fatal("expected budget to bound admission to 3 of 5 requested", n, e)
	}
	_, f, e := o.loadFleet(ctx)
	if e != nil || len(f.Pending) != 3 || f.Admission.Admitted != 3 {
		t.Fatal("partial admission must give the refused slots back to Admitted", f, e)
	}
	if f.Condition != "DemandObserved" {
		t.Fatal("a non-zero partial admission is still demand observed, not exhaustion", f.Condition)
	}

	o = &Operator{Config: cfg, Client: k, Store: s}
	// Demand above what's already admitted, so ordinary Reconcile alone would
	// want to admit more - only the exhausted budget should block it. A fully
	// refused call returns just the unchanged active count (3), matching the
	// existing refuse()/paused-path convention - never a fresh admission.
	n, e = o.HandleDesiredRunnerCount(ctx, 10)
	if e != nil || n != 3 {
		t.Fatal("budget already fully reserved today; nothing more should admit", n, e)
	}
	_, f, e = o.loadFleet(ctx)
	if e != nil || len(f.Pending) != 3 || f.Admission.Admitted != 3 {
		t.Fatal("refused admission must not grow Pending or leak Admitted", f, e)
	}
	if f.Condition != "BudgetExhausted" {
		t.Fatal("condition must distinguish budget exhaustion from ordinary demand", f, e)
	}
}

func TestZeroBudgetIsUnboundedAndDoesNotStampReservations(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 3600}
	cfg.Requirements = placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 1000, Providers: []string{"p"}, Regions: []string{"r"}, Policy: "lowest-price"}
	cfg.Catalog = placement.Catalog{Complete: map[string]bool{"p": true}, Offerings: []placement.Offering{{ID: "pool", Provider: "p", Region: "r", Zone: "z", Machine: "m", Image: "i", CPU: 1, MemoryMiB: 1, Architecture: "amd64", PriceMicros: 1, Currency: "USD", Spot: true, ObservedAt: time.Now()}}}
	s := &state.Kubernetes{Maps: k.CoreV1().ConfigMaps("test"), Owner: "test"}
	o := &Operator{Config: cfg, Client: k, Store: s}

	n, e := o.HandleDesiredRunnerCount(ctx, 5)
	if e != nil || n != 5 {
		t.Fatal("BudgetDailyMicros == 0 must behave exactly like no gate at all", n, e)
	}
	_, f, e := o.loadFleet(ctx)
	if e != nil || len(f.Reserved) != 0 {
		t.Fatal("no reservation should be stamped when no budget is configured", f.Reserved, e)
	}
}
