package operator

import (
	"context"
	"slices"
	"time"

	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/prices"
)

// awsPriceObserver is the surface internal/prices.AWSSpotClient (or a
// credential-bound wrapper around it) provides; declared locally, mirroring
// githubJobsClient in retry.go, so tests can substitute a fake without
// importing the concrete client's construction machinery.
type awsPriceObserver interface {
	Observe(ctx context.Context, region, zone, instanceType string) (prices.Quote, error)
}

// azurePriceObserver is the surface internal/prices.AzureSpotClient (or a
// wrapper around it) provides. It is declared separately from
// awsPriceObserver despite having an identical method set, so each
// provider's field and type name say plainly which cloud they observe -
// mirroring the two parallel refresh functions below rather than
// force-unifying names that merely happen to share a shape today.
type azurePriceObserver interface {
	Observe(ctx context.Context, region, zone, instanceType string) (prices.Quote, error)
}

// gcpPriceObserver is the surface internal/prices.GCPSkuClient (or a
// credential-bound wrapper around it) provides. Its shape is deliberately
// not "region, zone, instanceType" like awsPriceObserver/azurePriceObserver:
// a GCP price lookup resolves an exact, already human-pinned pair of Cloud
// Billing Catalog SKU IDs (see placement.Offering.GCPSkuRefs and
// docs/prices-gcp.md), and needs the offering's own CPU/MemoryMiB to
// combine the core and RAM unit prices into one offering price - see
// internal/prices.GCPSkuClient.Observe's own doc comment for why that
// combination from already-declared catalog fields is not a guess.
type gcpPriceObserver interface {
	Observe(ctx context.Context, coreSkuID, ramSkuID string, cpu, memoryMiB int) (prices.Quote, error)
}

// refreshAWSPrices replaces every AWS Spot offering's price and freshness
// with a live observation immediately before the catalog is used for
// admission. A failed observation zeroes that offering's ObservedAt so
// placement.Choose's freshness check deterministically excludes it this
// cycle - a failed live refresh must never silently keep the static
// catalog's stale-but-technically-valid timestamp usable. One offering's
// failure never affects any other AWS offering, non-AWS offerings are
// never touched at all, and neither is an on-demand (Spot: false) AWS
// offering - AWSSpotClient.Observe only ever returns a Spot price, so
// overwriting an on-demand offering's price with it would corrupt the one
// price placement.Choose's MaxPriceMicros ceiling actually compares
// against. When AWSPrices is nil the feature is fully inert: catalog is
// returned completely unmodified.
func (o *Operator) refreshAWSPrices(ctx context.Context, catalog placement.Catalog) placement.Catalog {
	if o.AWSPrices == nil {
		return catalog
	}
	// Clone before mutating: catalog.Offerings may alias o.Config.Catalog's
	// backing array (the in-memory, non-file catalog path returns it
	// directly), and refreshing must never corrupt that static source.
	offerings := slices.Clone(catalog.Offerings)
	for i := range offerings {
		if offerings[i].Provider != "aws" || !offerings[i].Spot {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, externalCallBudget)
		quote, err := o.AWSPrices.Observe(callCtx, offerings[i].Region, offerings[i].Zone, offerings[i].Machine)
		cancel()
		if err != nil {
			offerings[i].ObservedAt = time.Time{}
			continue
		}
		offerings[i].PriceMicros = quote.PriceMicros
		offerings[i].Currency = quote.Currency
		offerings[i].ObservedAt = quote.ObservedAt
	}
	catalog.Offerings = offerings
	return catalog
}

// refreshAzurePrices is refreshAWSPrices's exact Azure counterpart: same
// clone-before-mutate, same per-offering isolation, same "failure zeroes
// only ObservedAt" semantics, same "nil observer means fully inert"
// semantics. It is kept as a parallel function rather than folded together
// with refreshAWSPrices into one generalized "refreshPrices(provider
// string, observer priceObserver, ...)" helper: at two occurrences (AWS,
// then Azure) with an identical Observe(region, zone, machine) shape,
// unifying them would be exactly the premature abstraction this codebase
// avoids elsewhere. refreshGCPPrices below is the third provider-specific
// refresh this comment used to say would justify unifying - but its
// gcpPriceObserver shape (pinned SKU IDs plus CPU/MemoryMiB, no Spot-only
// restriction) is not a variation on this one, it is a different contract
// entirely, so forcing all three behind one signature would hide that
// difference rather than express it. The bar for a shared helper remains a
// fourth refresh whose observer shape actually matches one of these two.
func (o *Operator) refreshAzurePrices(ctx context.Context, catalog placement.Catalog) placement.Catalog {
	if o.AzurePrices == nil {
		return catalog
	}
	// Clone before mutating: see refreshAWSPrices's identical comment above.
	offerings := slices.Clone(catalog.Offerings)
	for i := range offerings {
		if offerings[i].Provider != "azure" || !offerings[i].Spot {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, externalCallBudget)
		quote, err := o.AzurePrices.Observe(callCtx, offerings[i].Region, offerings[i].Zone, offerings[i].Machine)
		cancel()
		if err != nil {
			offerings[i].ObservedAt = time.Time{}
			continue
		}
		offerings[i].PriceMicros = quote.PriceMicros
		offerings[i].Currency = quote.Currency
		offerings[i].ObservedAt = quote.ObservedAt
	}
	catalog.Offerings = offerings
	return catalog
}

// refreshGCPPrices replaces a GCP offering's price and freshness with a live
// observation of its own pinned SKU IDs, immediately before the catalog is
// used for admission - same clone-before-mutate, same per-offering
// isolation, same "failure zeroes only ObservedAt" semantics, same
// "nil observer means fully inert" semantics as refreshAWSPrices/
// refreshAzurePrices. It differs from both in exactly one respect: the
// per-offering gate is GCPSkuRefs != nil, not Spot - see
// placement.Offering.GCPSkuRefs and gcpPriceObserver's own doc comment for
// why a pinned SKU carries no such restriction (the human who pinned it
// already chose whichever SKU matches this offering's actual billing
// model, spot or on-demand; GCPSkuClient.Observe never assumes one or the
// other). An offering with Provider == "gcp" but GCPSkuRefs == nil is left
// on its static price exactly like every non-GCP offering - this is the
// deliberately narrow, no-guessing design docs/prices-gcp.md and
// docs/prices-gcp.background.md describe, not an oversight.
func (o *Operator) refreshGCPPrices(ctx context.Context, catalog placement.Catalog) placement.Catalog {
	if o.GCPPrices == nil {
		return catalog
	}
	// Clone before mutating: see refreshAWSPrices's identical comment above.
	offerings := slices.Clone(catalog.Offerings)
	for i := range offerings {
		if offerings[i].Provider != "gcp" || offerings[i].GCPSkuRefs == nil {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, externalCallBudget)
		quote, err := o.GCPPrices.Observe(callCtx, offerings[i].GCPSkuRefs.CoreSkuID, offerings[i].GCPSkuRefs.RamSkuID, offerings[i].CPU, offerings[i].MemoryMiB)
		cancel()
		if err != nil {
			offerings[i].ObservedAt = time.Time{}
			continue
		}
		offerings[i].PriceMicros = quote.PriceMicros
		offerings[i].Currency = quote.Currency
		offerings[i].ObservedAt = quote.ObservedAt
	}
	catalog.Offerings = offerings
	return catalog
}
