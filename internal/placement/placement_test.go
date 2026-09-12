package placement_test

import (
	"errors"
	p "github.com/tsouza/runnerscout/internal/placement"
	"math/rand"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)

func req() p.Requirements {
	return p.Requirements{CPU: 2, MemoryMiB: 4096, Architecture: "amd64", Providers: []string{"aws", "gcp"}, Regions: []string{"east"}, MaxPriceMicros: 1000000, Policy: "lowest-price"}
}
func offer(id, provider string, spot bool, price int64) p.Offering {
	return p.Offering{ID: id, Provider: provider, Region: "east", Zone: "east-a", Machine: "test", Image: "pinned", CPU: 2, MemoryMiB: 4096, Architecture: "amd64", Spot: spot, PriceMicros: price, Currency: "USD", ObservedAt: now}
}
func catalog() p.Catalog {
	return p.Catalog{Offerings: []p.Offering{offer("a", "aws", true, 100), offer("b", "gcp", true, 90), offer("c", "aws", false, 200)}, Complete: map[string]bool{"aws": true, "gcp": true}}
}
func TestFallbackRequiresDefinitiveExhaustion(t *testing.T) {
	r := req()
	r.AllowOnDemand = true
	for _, outcome := range []p.Outcome{p.Unknown, p.CoolingDown} {
		_, e := p.Choose(now, r, catalog(), map[string]p.Outcome{"a": p.CapacityRejected, "b": outcome})
		if !errors.Is(e, p.ErrIncomplete) {
			t.Fatalf("%s allowed fallback: %v", outcome, e)
		}
	}
	got, e := p.Choose(now, r, catalog(), map[string]p.Outcome{"a": p.CapacityRejected, "b": p.CapacityRejected})
	if e != nil || got.ID != "c" {
		t.Fatalf("fallback: %+v %v", got, e)
	}
	r.AllowOnDemand = false
	if _, e = p.Choose(now, r, catalog(), map[string]p.Outcome{"a": p.CapacityRejected, "b": p.CapacityRejected}); !errors.Is(e, p.ErrExhausted) {
		t.Fatal(e)
	}
}
func TestHardConstraintsAndFreshness(t *testing.T) {
	for _, mutate := range []func(*p.Catalog){func(c *p.Catalog) { c.Complete["gcp"] = false }, func(c *p.Catalog) { c.Offerings[0].ObservedAt = now.Add(-5*time.Minute - time.Nanosecond) }, func(c *p.Catalog) { c.Offerings[0].Currency = "EUR" }, func(c *p.Catalog) { c.Offerings[0].ObservedAt = now.Add(time.Nanosecond) }} {
		c := catalog()
		mutate(&c)
		if _, e := p.Choose(now, req(), c, nil); !errors.Is(e, p.ErrIncomplete) {
			t.Fatal(e)
		}
	}
	r := req()
	r.Capabilities = []string{"nested-virtualization"}
	if _, e := p.Choose(now, r, catalog(), nil); !errors.Is(e, p.ErrExhausted) {
		t.Fatal(e)
	}
	r = req()
	r.MaxPriceMicros = 89
	if _, e := p.Choose(now, r, catalog(), nil); !errors.Is(e, p.ErrExhausted) {
		t.Fatal(e)
	}
}

// The oracle independently scans generated catalogs using only the public input
// contract. It does not call placement's filtering or sorting implementation.
func TestIndependentSmallCatalogOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(1227))
	for trial := 0; trial < 1000; trial++ {
		r := req()
		c := p.Catalog{Complete: map[string]bool{"aws": true, "gcp": true}}
		want := ""
		best := int64(1 << 62)
		for i := 0; i < 8; i++ {
			o := offer(string(rune('a'+i)), "aws", true, int64(rng.Intn(1000)))
			o.CPU = 1 + rng.Intn(4)
			o.MemoryMiB = (1 + rng.Intn(8)) * 1024
			c.Offerings = append(c.Offerings, o)
			if o.CPU >= 2 && o.MemoryMiB >= 4096 && (o.PriceMicros < best || (o.PriceMicros == best && (want == "" || o.ID < want))) {
				best = o.PriceMicros
				want = o.ID
			}
		}
		rng.Shuffle(len(c.Offerings), func(i, j int) { c.Offerings[i], c.Offerings[j] = c.Offerings[j], c.Offerings[i] })
		got, e := p.Choose(now, r, c, nil)
		if want == "" {
			if !errors.Is(e, p.ErrExhausted) {
				t.Fatal(e)
			}
		} else if e != nil || got.ID != want {
			t.Fatalf("trial %d got %s want %s: %v", trial, got.ID, want, e)
		}
	}
}
