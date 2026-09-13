package provider

import (
	"context"
	"errors"

	"github.com/tsouza/runnerscout/internal/prices"
)

// AWSSpotPrices observes live EC2 Spot prices through the exact same
// explicit-only credential scope as VM provisioning uses. Unlike a
// provisioning session, which is bound to one region for its whole
// lifetime, Observe resolves credentials fresh for whatever region it is
// asked about, since one AWS identity's spot price catalog can span many
// regions in a single admission cycle.
type AWSSpotPrices struct {
	sdk *AWSSDK
}

// SpotPrices returns a live price observer bound to this AWSSDK's
// credential scope. It performs no network calls itself.
func (s *AWSSDK) SpotPrices() *AWSSpotPrices {
	return &AWSSpotPrices{sdk: s}
}

func (o *AWSSpotPrices) Observe(ctx context.Context, region, zone, instanceType string) (prices.Quote, error) {
	if o == nil || o.sdk == nil {
		return prices.Quote{}, errors.New("AWS spot price observer unavailable")
	}
	scope, err := o.sdk.resolvedScope(region)
	if err != nil {
		return prices.Quote{}, err
	}
	value, err := scope.retrieve(ctx, region)
	if err != nil {
		return prices.Quote{}, err
	}
	client := &prices.AWSSpotClient{Credentials: frozenAWSCredentials(value), HTTPClient: scope.client, Endpoint: scope.endpoint}
	return client.Observe(ctx, region, zone, instanceType)
}
