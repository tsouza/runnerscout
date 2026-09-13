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

// refreshAWSPrices replaces every AWS offering's price and freshness with a
// live observation immediately before the catalog is used for admission.
// A failed observation zeroes that offering's ObservedAt so
// placement.Choose's freshness check deterministically excludes it this
// cycle - a failed live refresh must never silently keep the static
// catalog's stale-but-technically-valid timestamp usable. One offering's
// failure never affects any other AWS offering, and non-AWS offerings are
// never touched at all. When AWSPrices is nil the feature is fully inert:
// catalog is returned completely unmodified.
func (o *Operator) refreshAWSPrices(ctx context.Context, catalog placement.Catalog) placement.Catalog {
	if o.AWSPrices == nil {
		return catalog
	}
	// Clone before mutating: catalog.Offerings may alias o.Config.Catalog's
	// backing array (the in-memory, non-file catalog path returns it
	// directly), and refreshing must never corrupt that static source.
	offerings := slices.Clone(catalog.Offerings)
	for i := range offerings {
		if offerings[i].Provider != "aws" {
			continue
		}
		quote, err := o.AWSPrices.Observe(ctx, offerings[i].Region, offerings[i].Zone, offerings[i].Machine)
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
